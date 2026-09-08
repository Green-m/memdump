# memdump

`memdump` 是一个 Linux 进程内存导出及扫描工具。它读取 `/proc/<pid>/maps`
和 `/proc/<pid>/mem`，既可以导出完整内存，也可以直接流式提取或过滤其中的
可打印字符串，避免把大体积内存完整写入磁盘。

## 构建

```bash
./scripts/build.sh
```

脚本默认生成 Linux `amd64`、`arm64`、`386`、`arm/v7`、`riscv64` 和
`loong64` 六种静态二进制文件，统一存放在 `bin/`，并生成
`bin/SHA256SUMS`。也可以只构建指定目标：

```bash
./scripts/build.sh linux/amd64 linux/arm64
```

## 使用

```bash
./bin/memdump-linux-amd64 [选项] <pid> <输出文件>

# 默认模式：完整内存 dump
sudo ./bin/memdump-linux-amd64 -stop 1234 process.dump

# strings 模式：直接输出所有可打印 ASCII 字符串，不生成完整 dump
sudo ./bin/memdump-linux-amd64 -strings 1234 -

# 正则模式：只将匹配的可打印字符串写入结果文件
sudo ./bin/memdump-linux-amd64 -regex 'token=[[:alnum:]]+' 1234 matches.txt
```

工具有三种互斥的运行形式：

1. 不指定扫描参数时完整导出内存，并生成 `process.dump.maps` 地址索引。
2. `-strings` 流式提取长度不小于 `-min-string` 的可打印 ASCII 字符串。
3. `-regex EXPR` 提取字符串后使用 Go 正则表达式过滤；普通文本也可以直接
   作为正则表达式，因此不再单独提供固定字符串模式。

扫描模式只输出 `虚拟地址 可打印字符串`，不会创建完整 dump 或 `.maps` 文件。
输出文件指定为 `-` 时结果写入标准输出，可以继续通过管道处理：

```bash
sudo ./bin/memdump-linux-amd64 -strings 1234 - | less
sudo ./bin/memdump-linux-amd64 -regex 'https?://[^ ]+' 1234 - > urls.txt
```

### 正则扫描示例

`-regex` 使用 Go 正则表达式过滤完整的可打印 ASCII 字符串。建议在
Shell 中始终用单引号包住表达式，避免 `*`、`$` 等字符被 Shell
提前解释。

```bash
# 包含任意连续 20 个 Base64 字母表字符的字符串
sudo ./bin/memdump-linux-amd64 -regex '[A-Za-z0-9+/]{20}' 1234 base64-20.txt

# 连续 20 个或更多 Base64 字母表字符，可带 0～2 个填充符
sudo ./bin/memdump-linux-amd64 -regex '[A-Za-z0-9+/]{20,}={0,2}' 1234 base64.txt

# 恰好 20 个 Base64 字母表字符，两端不能紧邻同一字母表中的字符
sudo ./bin/memdump-linux-amd64 -regex '(^|[^A-Za-z0-9+/])[A-Za-z0-9+/]{20}([^A-Za-z0-9+/]|$)' 1234 base64-exact-20.txt

# URL-safe Base64 或长随机 token
sudo ./bin/memdump-linux-amd64 -regex '[A-Za-z0-9_-]{20,}={0,2}' 1234 tokens.txt

# JWT 样式的三段式 token
sudo ./bin/memdump-linux-amd64 -regex '[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}' 1234 jwt.txt

# token/secret/password 等键值对，键名不区分大小写
sudo ./bin/memdump-linux-amd64 -regex '(?i)(token|secret|password)[ ]*[:=][ ]*[A-Za-z0-9_+/=-]{8,}' 1234 secrets.txt

# 32 个或更多连续十六进制字符
sudo ./bin/memdump-linux-amd64 -regex '[A-Fa-f0-9]{32,}' 1234 hex.txt
```

`{20}` 约束的是“匹配子串恰好有 20 个字符”，不代表整个候选字符串
只能有 20 个字符。例如 30 个连续 Base64 字母表字符中仍然包含一个
20 字符的匹配；如需排除更长的连续串，使用上面带两端边界的写法。

此外，这些表达式只识别“Base64 样式”的字符集，不会验证内容是否真的
Base64 编码。当一个可打印字符串中的任意位置匹配正则时，memdump 输出的
是该完整字符串及其起始虚拟地址，而不是仅输出正则匹配到的部分。

默认完整 dump 的映射索引每一行依次记录：输出文件范围、进程虚拟地址范围、
权限、原文件偏移、设备号、inode 和映射名称。由于输出是各映射的紧凑拼接
文件，分析某个虚拟地址时应使用该索引换算偏移。

常用选项：

- `-anonymous-only`：只导出匿名映射、堆、栈等方括号标记的映射。
- `-strings`：流式提取全部可打印字符串。
- `-regex EXPR`：只输出正则匹配的可打印字符串。
- `-min-string N`：设置扫描模式的最小字符串长度，默认是 4。
- `-stop`：导出期间向目标进程发送 `SIGSTOP`，结束时保证发送 `SIGCONT`。
- `-strict`：任一页无法读取就报错；默认以零填充并继续。
- `-maps -`：不生成映射索引。
- `-force`：覆盖已有输出。

读取其他进程的 `/proc/<pid>/mem` 受 Linux ptrace 权限控制。同一用户也可能被
Yama `ptrace_scope` 限制；此时请使用具备 `CAP_SYS_PTRACE` 的账户，或在符合
系统安全策略的前提下调整相关配置。目标进程在导出期间仍修改内存会造成快照
不一致，需要一致快照时使用 `-stop`。
