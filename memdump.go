// memdump dumps the readable virtual memory mappings of a Linux process.
package main

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const defaultChunkSize = 1024 * 1024

type operationMode uint8

const (
	modeDump operationMode = iota
	modeStrings
	modeRegexp
)

type options struct {
	pid           int
	outputPath    string
	mapPath       string
	chunkSize     int
	showAddress   bool
	force         bool
	strict        bool
	quiet         bool
	anonymousOnly bool
	stop          bool
	mode          operationMode
	stringsMode   bool
	regexpText    string
	minStringLen  int
}

type mapping struct {
	start       uint64
	end         uint64
	permissions string
	offset      uint64
	device      string
	inode       uint64
	pathname    string
}

func (m mapping) size() uint64 {
	return m.end - m.start
}

func (m mapping) readable() bool {
	return len(m.permissions) > 0 && m.permissions[0] == 'r'
}

func (m mapping) anonymous() bool {
	return m.pathname == "" || (strings.HasPrefix(m.pathname, "[") && strings.HasSuffix(m.pathname, "]"))
}

type dumpStats struct {
	regions         int
	bytesWritten    uint64
	bytesScanned    uint64
	unreadableBytes uint64
	matches         uint64
}

func main() {
	opts, err := parseOptions(os.Args[1:])
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fmt.Fprintln(os.Stdout, usageText())
			return
		}
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}

	stats, err := run(opts)
	if err != nil {
		fmt.Fprintf(os.Stderr, "memdump: %v\n", err)
		os.Exit(1)
	}

	if !opts.quiet && opts.mode == modeDump {
		fmt.Fprintf(os.Stderr, "完成: %d 个内存区域, %s 已写入 %s", stats.regions, formatBytes(stats.bytesWritten), opts.outputPath)
		if stats.unreadableBytes > 0 {
			fmt.Fprintf(os.Stderr, "（其中 %s 无法读取，已填充为 0）", formatBytes(stats.unreadableBytes))
		}
		fmt.Fprintln(os.Stderr)
		if opts.mapPath != "-" {
			fmt.Fprintf(os.Stderr, "映射索引: %s\n", opts.mapPath)
		}
	} else if !opts.quiet {
		destination := opts.outputPath
		if destination == "-" {
			destination = "标准输出"
		}
		action := "提取"
		if opts.mode == modeRegexp {
			action = "匹配"
		}
		fmt.Fprintf(os.Stderr, "完成: 扫描 %d 个内存区域（%s），%s %d 条，结果写入 %s",
			stats.regions, formatBytes(stats.bytesScanned), action, stats.matches, destination)
		if stats.unreadableBytes > 0 {
			fmt.Fprintf(os.Stderr, "（跳过 %s 无法读取的数据）", formatBytes(stats.unreadableBytes))
		}
		fmt.Fprintln(os.Stderr)
	}
}

func parseOptions(args []string) (options, error) {
	var opts options
	fs := flag.NewFlagSet("memdump", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.BoolVar(&opts.showAddress, "address", false, "扫描模式下输出字符串起始虚拟地址")
	fs.IntVar(&opts.chunkSize, "chunk-size", defaultChunkSize, "每次读取的字节数")
	fs.StringVar(&opts.mapPath, "maps", "", "映射索引文件路径；默认 <输出文件>.maps，- 表示禁用")
	fs.BoolVar(&opts.force, "force", false, "覆盖已存在的输出文件")
	fs.BoolVar(&opts.strict, "strict", false, "任何内存页读取失败时立即退出")
	fs.BoolVar(&opts.quiet, "quiet", false, "不显示进度信息")
	fs.BoolVar(&opts.anonymousOnly, "anonymous-only", false, "只导出匿名及方括号标记的映射")
	fs.BoolVar(&opts.stop, "stop", false, "导出期间暂停目标进程")
	fs.BoolVar(&opts.stringsMode, "strings", false, "提取所有可打印字符串，不保存完整内存")
	fs.StringVar(&opts.regexpText, "regex", "", "只输出匹配 Go 正则表达式的可打印字符串")
	fs.IntVar(&opts.minStringLen, "min-string", 4, "扫描模式下可打印字符串的最小长度")
	fs.Usage = func() {}

	if err := fs.Parse(args); errors.Is(err, flag.ErrHelp) {
		return opts, flag.ErrHelp
	} else if err != nil {
		return opts, fmt.Errorf("%v\n\n%s", err, usageText())
	}
	if fs.NArg() != 2 {
		return opts, errors.New(usageText())
	}

	pid64, err := strconv.ParseInt(fs.Arg(0), 10, 32)
	if err != nil || pid64 <= 0 {
		return opts, fmt.Errorf("PID 必须是正整数: %q", fs.Arg(0))
	}
	if opts.chunkSize <= 0 {
		return opts, errors.New("-chunk-size 必须大于 0")
	}
	if opts.minStringLen <= 0 {
		return opts, errors.New("-min-string 必须大于 0")
	}
	if opts.stringsMode && opts.regexpText != "" {
		return opts, errors.New("-strings 和 -regex 不能同时使用")
	}
	if opts.stringsMode {
		opts.mode = modeStrings
	} else if opts.regexpText != "" {
		if _, err := regexp.Compile(opts.regexpText); err != nil {
			return opts, fmt.Errorf("无效的正则表达式: %w", err)
		}
		opts.mode = modeRegexp
	}
	if opts.mode == modeDump && opts.showAddress {
		return opts, errors.New("-address 只能与 -strings 或 -regex 一起使用")
	}

	opts.pid = int(pid64)
	opts.outputPath = fs.Arg(1)
	if opts.mode != modeDump {
		if opts.mapPath != "" && opts.mapPath != "-" {
			return opts, errors.New("匹配模式不生成映射索引，请移除 -maps 或使用 -maps -")
		}
		opts.mapPath = "-"
	} else if opts.outputPath == "-" {
		return opts, errors.New("全量导出不能写入标准输出，请指定输出文件")
	} else if opts.mapPath == "" {
		opts.mapPath = opts.outputPath + ".maps"
	}
	if opts.mapPath != "-" && samePath(opts.outputPath, opts.mapPath) {
		return opts, errors.New("内存输出文件和映射索引文件不能是同一路径")
	}
	return opts, nil
}

func usageText() string {
	return `用法: memdump [选项] <pid> <输出文件>

默认将 Linux 进程的可读内存映射写入原始二进制文件。使用 -strings 或
-regex 时直接流式扫描进程内存，只输出匹配结果，不保存完整内存。

选项:
  -address          扫描模式下输出字符串起始虚拟地址
  -anonymous-only   只导出匿名及方括号标记的映射
  -chunk-size N     每次读取 N 字节（默认 1048576）
  -force            覆盖已有文件
  -maps PATH        映射索引路径（默认 <输出文件>.maps，- 表示禁用）
  -min-string N     可打印字符串的最小长度（默认 4）
  -quiet            不显示进度
  -regex EXPR       正则匹配，输出包含匹配项的可打印字符串
  -stop             导出期间暂停目标进程
  -strict           遇到无法读取的内存页时失败
  -strings          提取所有可打印字符串

正则示例:
  -regex '[A-Za-z0-9+/]{20}'
      包含任意连续 20 个 Base64 字母表字符
  -regex '[A-Za-z0-9+/]{20,}={0,2}'
      包含 20 个或更多 Base64 字母表字符，可带填充符
  -regex '[A-Za-z0-9_-]{20,}={0,2}'
      URL-safe Base64 或长随机 token
  -regex '[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}'
      JWT 样式的三段式 token
  -regex '(?i)(token|secret|password)[ ]*[:=][ ]*[A-Za-z0-9_+/=-]{8,}'
      token/secret/password 等键值对
  -regex '[A-Fa-f0-9]{32,}'
      32 个或更多连续十六进制字符

{20} 也会匹配更长连续串中的 20 字符子串。扫描模式默认只输出完整
可打印字符串，而不是仅输出正则命中的部分；使用 -address 可在每行前输出
字符串起始虚拟地址。输出文件为 - 时写到标准输出。`
}

func run(opts options) (stats dumpStats, returnErr error) {
	if runtime.GOOS != "linux" {
		return stats, fmt.Errorf("只支持 Linux，当前系统是 %s", runtime.GOOS)
	}
	if opts.stop && opts.pid == os.Getpid() {
		return stats, errors.New("不能使用 -stop 导出 memdump 自身，否则进程将无法恢复")
	}
	if opts.outputPath != "-" {
		if err := preflightOutput(opts.outputPath, opts.force); err != nil {
			return stats, err
		}
	}
	if opts.mapPath != "-" {
		if err := preflightOutput(opts.mapPath, opts.force); err != nil {
			return stats, err
		}
	}

	resume, err := stopProcess(opts.pid, opts.stop)
	if err != nil {
		return stats, err
	}
	defer func() {
		if err := resume(); err != nil && returnErr == nil {
			returnErr = err
		}
	}()

	mapsPath := fmt.Sprintf("/proc/%d/maps", opts.pid)
	mapsFile, err := os.Open(mapsPath)
	if err != nil {
		return stats, processAccessError(mapsPath, err)
	}
	mappings, err := parseMappings(mapsFile, opts.anonymousOnly)
	closeErr := mapsFile.Close()
	if err != nil {
		return stats, fmt.Errorf("解析 %s: %w", mapsPath, err)
	}
	if closeErr != nil {
		return stats, fmt.Errorf("关闭 %s: %w", mapsPath, closeErr)
	}
	if len(mappings) == 0 {
		return stats, errors.New("没有找到符合条件的可读内存映射")
	}

	var total uint64
	for _, m := range mappings {
		if opts.mode == modeDump {
			if math.MaxUint64-total < m.size() || total+m.size() > math.MaxInt64 {
				return stats, errors.New("内存映射总大小超出当前工具支持范围")
			}
			total += m.size()
		}
	}

	memPath := fmt.Sprintf("/proc/%d/mem", opts.pid)
	memFile, err := os.Open(memPath)
	if err != nil {
		return stats, processAccessError(memPath, err)
	}
	defer func() {
		if err := memFile.Close(); err != nil && returnErr == nil {
			returnErr = fmt.Errorf("关闭 %s: %w", memPath, err)
		}
	}()

	if opts.mode == modeDump {
		return runDump(opts, procMemReader{file: memFile}, mappings)
	}
	return runScan(opts, procMemReader{file: memFile}, mappings)
}

// procMemReader bypasses os.File.ReadAt's rejection of negative int64 offsets.
// Linux marks /proc/PID/mem as accepting unsigned offsets, which is required for
// special mappings such as x86-64's ffffffffff600000 [vsyscall] page.
type procMemReader struct {
	file *os.File
}

func (r procMemReader) ReadAt(p []byte, off int64) (n int, err error) {
	for len(p) > 0 {
		read, readErr := syscall.Pread(int(r.file.Fd()), p, off)
		if read < 0 {
			read = 0
		}
		if read > len(p) {
			return n, errors.New("内存读取器返回了无效的字节数")
		}
		n += read
		p = p[read:]
		off += int64(read)

		if errors.Is(readErr, syscall.EINTR) {
			continue
		}
		if readErr != nil {
			return n, &os.PathError{Op: "read", Path: r.file.Name(), Err: readErr}
		}
		if read == 0 {
			return n, io.EOF
		}
	}
	return n, nil
}

func runDump(opts options, memFile io.ReaderAt, mappings []mapping) (stats dumpStats, returnErr error) {
	output, err := createOutput(opts.outputPath, opts.force)
	if err != nil {
		return stats, err
	}
	outputOK := false
	defer func() {
		if err := output.Close(); err != nil && returnErr == nil {
			returnErr = fmt.Errorf("关闭输出文件: %w", err)
		}
		if !outputOK {
			_ = os.Remove(opts.outputPath)
		}
	}()

	var mapOutput *os.File
	mapOK := false
	if opts.mapPath != "-" {
		mapOutput, err = createOutput(opts.mapPath, opts.force)
		if err != nil {
			return stats, err
		}
		defer func() {
			if err := mapOutput.Close(); err != nil && returnErr == nil {
				returnErr = fmt.Errorf("关闭映射索引文件: %w", err)
			}
			if !mapOK {
				_ = os.Remove(opts.mapPath)
			}
		}()
		if _, err := fmt.Fprintln(mapOutput, "# output_start-output_end virtual_start-virtual_end perms file_offset device inode pathname"); err != nil {
			return stats, fmt.Errorf("写入映射索引文件: %w", err)
		}
	}

	buffer := make([]byte, opts.chunkSize)
	pageSize := os.Getpagesize()
	var outputOffset uint64
	for i, m := range mappings {
		showProgress(opts, i, len(mappings), m)
		unreadable, err := dumpMapping(memFile, output, m, buffer, pageSize, opts.strict)
		if err != nil {
			finishProgress(opts)
			return stats, err
		}
		if mapOutput != nil {
			if err := writeMapRecord(mapOutput, outputOffset, m); err != nil {
				return stats, err
			}
		}

		stats.regions++
		stats.bytesWritten += m.size()
		stats.unreadableBytes += unreadable
		outputOffset += m.size()
	}
	finishProgress(opts)

	if err := output.Sync(); err != nil {
		return stats, fmt.Errorf("同步输出文件: %w", err)
	}
	if mapOutput != nil {
		if err := mapOutput.Sync(); err != nil {
			return stats, fmt.Errorf("同步映射索引文件: %w", err)
		}
		mapOK = true
	}
	outputOK = true
	return stats, nil
}

func runScan(opts options, memFile io.ReaderAt, mappings []mapping) (stats dumpStats, returnErr error) {
	var destination io.Writer = os.Stdout
	var output *os.File
	outputOK := opts.outputPath == "-"
	if opts.outputPath != "-" {
		var err error
		output, err = createOutput(opts.outputPath, opts.force)
		if err != nil {
			return stats, err
		}
		destination = output
		defer func() {
			if err := output.Close(); err != nil && returnErr == nil {
				returnErr = fmt.Errorf("关闭输出文件: %w", err)
			}
			if !outputOK {
				_ = os.Remove(opts.outputPath)
			}
		}()
	}

	var matcher func([]byte) bool
	if opts.mode == modeRegexp {
		re, err := regexp.Compile(opts.regexpText)
		if err != nil {
			return stats, fmt.Errorf("编译正则表达式: %w", err)
		}
		matcher = re.Match
	} else {
		matcher = func([]byte) bool { return true }
	}

	writer := bufio.NewWriterSize(destination, 64*1024)
	collector := printableStringCollector{
		output:      writer,
		minLength:   opts.minStringLen,
		matches:     matcher,
		showAddress: opts.showAddress,
	}
	buffer := make([]byte, opts.chunkSize)
	pageSize := os.Getpagesize()
	for i, m := range mappings {
		showProgress(opts, i, len(mappings), m)
		unreadable, err := scanMapping(memFile, m, buffer, pageSize, opts.strict, &collector)
		if err != nil {
			finishProgress(opts)
			return stats, err
		}
		stats.regions++
		stats.bytesScanned += m.size()
		stats.unreadableBytes += unreadable
	}
	finishProgress(opts)

	if err := writer.Flush(); err != nil {
		return stats, fmt.Errorf("刷新匹配结果: %w", err)
	}
	if output != nil {
		if err := output.Sync(); err != nil {
			return stats, fmt.Errorf("同步输出文件: %w", err)
		}
	}
	stats.matches = collector.matchCount
	outputOK = true
	return stats, nil
}

func showProgress(opts options, index, total int, m mapping) {
	if !opts.quiet {
		fmt.Fprintf(os.Stderr, "\r[%d/%d] %016x-%016x %-4s %s", index+1, total, m.start, m.end, m.permissions, truncatePath(m.pathname, 36))
	}
}

func finishProgress(opts options) {
	if !opts.quiet {
		fmt.Fprintln(os.Stderr)
	}
}

func parseMappings(r io.Reader, anonymousOnly bool) ([]mapping, error) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	var mappings []mapping
	lineNumber := 0
	for scanner.Scan() {
		lineNumber++
		m, err := parseMappingLine(scanner.Text())
		if err != nil {
			return nil, fmt.Errorf("第 %d 行: %w", lineNumber, err)
		}
		if !m.readable() || (anonymousOnly && !m.anonymous()) {
			continue
		}
		mappings = append(mappings, m)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return mappings, nil
}

func parseMappingLine(line string) (mapping, error) {
	var m mapping
	fields := strings.Fields(line)
	if len(fields) < 5 {
		return m, fmt.Errorf("无效的 maps 记录: %q", line)
	}

	addresses := strings.SplitN(fields[0], "-", 2)
	if len(addresses) != 2 {
		return m, fmt.Errorf("无效的地址范围: %q", fields[0])
	}
	start, err := strconv.ParseUint(addresses[0], 16, 64)
	if err != nil {
		return m, fmt.Errorf("无效的起始地址 %q: %w", addresses[0], err)
	}
	end, err := strconv.ParseUint(addresses[1], 16, 64)
	if err != nil || end <= start {
		return m, fmt.Errorf("无效的结束地址 %q", addresses[1])
	}
	offset, err := strconv.ParseUint(fields[2], 16, 64)
	if err != nil {
		return m, fmt.Errorf("无效的文件偏移 %q: %w", fields[2], err)
	}
	inode, err := strconv.ParseUint(fields[4], 10, 64)
	if err != nil {
		return m, fmt.Errorf("无效的 inode %q: %w", fields[4], err)
	}

	m = mapping{
		start:       start,
		end:         end,
		permissions: fields[1],
		offset:      offset,
		device:      fields[3],
		inode:       inode,
	}
	if len(fields) > 5 {
		m.pathname = strings.Join(fields[5:], " ")
	}
	return m, nil
}

func dumpMapping(mem io.ReaderAt, output io.Writer, m mapping, buffer []byte, pageSize int, strict bool) (uint64, error) {
	var unreadable uint64
	for address := m.start; address < m.end; {
		remaining := m.end - address
		want := len(buffer)
		if remaining < uint64(want) {
			want = int(remaining)
		}
		chunk := buffer[:want]

		missing, err := readMemoryChunk(mem, chunk, address, pageSize, strict)
		if err != nil {
			return unreadable, err
		}
		unreadable += missing

		if err := writeAll(output, chunk); err != nil {
			return unreadable, fmt.Errorf("写入输出文件: %w", err)
		}
		address += uint64(want)
	}
	return unreadable, nil
}

type printableStringCollector struct {
	output      io.Writer
	minLength   int
	matches     func([]byte) bool
	showAddress bool
	current     []byte
	start       uint64
	matchCount  uint64
}

func (c *printableStringCollector) consume(address uint64, data []byte) error {
	for i, b := range data {
		if b >= 0x20 && b <= 0x7e {
			if len(c.current) == 0 {
				c.start = address + uint64(i)
			}
			c.current = append(c.current, b)
			continue
		}
		if err := c.finish(); err != nil {
			return err
		}
	}
	return nil
}

func (c *printableStringCollector) finish() error {
	if len(c.current) >= c.minLength && c.matches(c.current) {
		var err error
		if c.showAddress {
			_, err = fmt.Fprintf(c.output, "%016x %s\n", c.start, c.current)
		} else {
			_, err = fmt.Fprintf(c.output, "%s\n", c.current)
		}
		if err != nil {
			return fmt.Errorf("写入匹配结果: %w", err)
		}
		c.matchCount++
	}
	if cap(c.current) > 4*defaultChunkSize {
		c.current = nil
	} else {
		c.current = c.current[:0]
	}
	return nil
}

func scanMapping(mem io.ReaderAt, m mapping, buffer []byte, pageSize int, strict bool, collector *printableStringCollector) (uint64, error) {
	var unreadable uint64
	for address := m.start; address < m.end; {
		remaining := m.end - address
		want := len(buffer)
		if remaining < uint64(want) {
			want = int(remaining)
		}
		chunk := buffer[:want]
		missing, err := readMemoryChunk(mem, chunk, address, pageSize, strict)
		if err != nil {
			return unreadable, err
		}
		unreadable += missing
		if err := collector.consume(address, chunk); err != nil {
			return unreadable, err
		}
		address += uint64(want)
	}
	if err := collector.finish(); err != nil {
		return unreadable, err
	}
	return unreadable, nil
}

func readMemoryChunk(mem io.ReaderAt, dst []byte, address uint64, pageSize int, strict bool) (uint64, error) {
	n, readErr := mem.ReadAt(dst, int64(address))
	if n == len(dst) && readErr == nil {
		return 0, nil
	}
	if strict {
		if readErr == nil {
			readErr = io.ErrUnexpectedEOF
		}
		return 0, fmt.Errorf("读取内存 %x-%x: %w", address, address+uint64(len(dst)), readErr)
	}
	clear(dst)
	return readPageByPage(mem, dst, address, pageSize)
}

func readPageByPage(mem io.ReaderAt, dst []byte, address uint64, pageSize int) (uint64, error) {
	var missing uint64
	for pos := 0; pos < len(dst); pos += pageSize {
		end := pos + pageSize
		if end > len(dst) {
			end = len(dst)
		}
		page := dst[pos:end]
		n, _ := mem.ReadAt(page, int64(address)+int64(pos))
		if n < 0 || n > len(page) {
			return missing, errors.New("内存读取器返回了无效的字节数")
		}
		if n < len(page) {
			clear(page[n:])
			missing += uint64(len(page) - n)
		}
	}
	return missing, nil
}

func writeAll(w io.Writer, data []byte) error {
	for len(data) > 0 {
		n, err := w.Write(data)
		if err != nil {
			return err
		}
		if n <= 0 || n > len(data) {
			return io.ErrShortWrite
		}
		data = data[n:]
	}
	return nil
}

func writeMapRecord(w io.Writer, outputOffset uint64, m mapping) error {
	_, err := fmt.Fprintf(w, "%016x-%016x %016x-%016x %s %08x %s %d %q\n",
		outputOffset, outputOffset+m.size(), m.start, m.end, m.permissions,
		m.offset, m.device, m.inode, m.pathname)
	if err != nil {
		return fmt.Errorf("写入映射索引文件: %w", err)
	}
	return nil
}

func preflightOutput(path string, force bool) error {
	info, err := os.Stat(path)
	if err == nil {
		if info.IsDir() {
			return fmt.Errorf("输出路径是目录: %s", path)
		}
		if !force {
			return fmt.Errorf("文件已存在: %s（使用 -force 覆盖）", path)
		}
		return nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("检查输出路径 %s: %w", path, err)
	}
	parent := filepath.Dir(path)
	if info, err := os.Stat(parent); err != nil {
		return fmt.Errorf("检查输出目录 %s: %w", parent, err)
	} else if !info.IsDir() {
		return fmt.Errorf("输出文件的父路径不是目录: %s", parent)
	}
	return nil
}

func createOutput(path string, force bool) (*os.File, error) {
	flags := os.O_WRONLY | os.O_CREATE
	if force {
		flags |= os.O_TRUNC
	} else {
		flags |= os.O_EXCL
	}
	f, err := os.OpenFile(path, flags, 0o600)
	if err != nil {
		return nil, fmt.Errorf("创建输出文件 %s: %w", path, err)
	}
	return f, nil
}

func stopProcess(pid int, enabled bool) (func() error, error) {
	noop := func() error { return nil }
	if !enabled {
		return noop, nil
	}

	alreadyStopped, err := isProcessStopped(pid)
	if err != nil {
		return noop, err
	}
	if alreadyStopped {
		return noop, nil
	}
	if err := syscall.Kill(pid, syscall.SIGSTOP); err != nil {
		return noop, fmt.Errorf("暂停进程 %d: %w", pid, err)
	}

	resume := func() error {
		if err := syscall.Kill(pid, syscall.SIGCONT); err != nil && !errors.Is(err, syscall.ESRCH) {
			return fmt.Errorf("恢复进程 %d: %w", pid, err)
		}
		return nil
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		stopped, err := isProcessStopped(pid)
		if err != nil {
			_ = resume()
			return noop, err
		}
		if stopped {
			return resume, nil
		}
		time.Sleep(10 * time.Millisecond)
	}
	_ = resume()
	return noop, fmt.Errorf("等待进程 %d 暂停超时", pid)
}

func isProcessStopped(pid int) (bool, error) {
	statusPath := fmt.Sprintf("/proc/%d/status", pid)
	data, err := os.ReadFile(statusPath)
	if err != nil {
		return false, processAccessError(statusPath, err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "State:") {
			fields := strings.Fields(line)
			return len(fields) >= 2 && (fields[1] == "T" || fields[1] == "t"), nil
		}
	}
	return false, fmt.Errorf("%s 中没有进程状态", statusPath)
}

func processAccessError(path string, err error) error {
	if errors.Is(err, os.ErrPermission) {
		return fmt.Errorf("访问 %s 被拒绝: %w；请确认目标进程属于当前用户并允许 ptrace，或使用具备 CAP_SYS_PTRACE 的账户", path, err)
	}
	if errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("进程不存在或已退出（%s）", path)
	}
	return fmt.Errorf("打开 %s: %w", path, err)
}

func samePath(a, b string) bool {
	aa, errA := filepath.Abs(filepath.Clean(a))
	bb, errB := filepath.Abs(filepath.Clean(b))
	return errA == nil && errB == nil && aa == bb
}

func truncatePath(path string, width int) string {
	if path == "" {
		return "[anonymous]"
	}
	runes := []rune(path)
	if len(runes) <= width {
		return path
	}
	return "…" + string(runes[len(runes)-width+1:])
}

func formatBytes(n uint64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := uint64(unit), 0
	for value := n / unit; value >= unit && exp < 5; value /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}
