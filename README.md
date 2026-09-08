# memdump

`memdump` 是一个 Linux 进程内存导出工具。它读取 `/proc/<pid>/maps` 和
`/proc/<pid>/mem`，把所有可读映射顺序写入一个原始二进制文件，并生成一份
地址到输出偏移的索引。

## 构建

```bash
CGO_ENABLED=0 go build -trimpath -o memdump .
```

## 使用

```bash
./memdump [选项] <pid> <输出文件>

# 示例
sudo ./memdump -stop 1234 process.dump
strings process.dump | less
```

默认还会生成 `process.dump.maps`。其每一行依次记录：输出文件范围、进程虚拟
地址范围、权限、原文件偏移、设备号、inode 和映射名称。由于输出是各映射的
紧凑拼接文件，分析某个虚拟地址时应使用该索引换算偏移。

常用选项：

- `-anonymous-only`：只导出匿名映射、堆、栈等方括号标记的映射。
- `-stop`：导出期间向目标进程发送 `SIGSTOP`，结束时保证发送 `SIGCONT`。
- `-strict`：任一页无法读取就报错；默认以零填充并继续。
- `-maps -`：不生成映射索引。
- `-force`：覆盖已有输出。

读取其他进程的 `/proc/<pid>/mem` 受 Linux ptrace 权限控制。同一用户也可能被
Yama `ptrace_scope` 限制；此时请使用具备 `CAP_SYS_PTRACE` 的账户，或在符合
系统安全策略的前提下调整相关配置。目标进程在导出期间仍修改内存会造成快照
不一致，需要一致快照时使用 `-stop`。
