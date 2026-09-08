# memdump

`memdump` 是一个 Linux 进程内存导出工具。它读取 `/proc/<pid>/maps` 和
`/proc/<pid>/mem`，把所有可读映射顺序写入一个原始二进制文件，并生成一份
地址到输出偏移的索引。

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

# 示例
sudo ./bin/memdump-linux-amd64 -stop 1234 process.dump
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
