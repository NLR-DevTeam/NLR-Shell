<h1 align="center">
    <img align="center" src="https://github.com/NLR-DevTeam/NLR-Shell/blob/main/build/icon.png">
    <p>NLR Shell</p>
</h1>
<p align="center">轻量、纯净、好用的 SSH 管理工具</p>

[![license](https://img.shields.io/badge/license-MIT-blue.svg)](https://github.com/NLR-DevTeam/NLR-Shell/blob/main/LICENSE)
<img width="2560" height="1600" alt="3ACDFC3056560D55E0486CEA3859E368" src="https://github.com/user-attachments/assets/f7e4f4fa-0d14-4eda-91ef-038e6a362b7a" />

## 功能

- 支持快速输入`user@ip`连接服务器
- 支持查看服务器各项数据：
  1. 服务器基本信息
  2. CPU 运行状态，各个核心占用情况
  3. 内存利用率
  4. 网络状况（上传，下载占用）
  5. 磁盘占用
  6. 进程
- 方便的文件管理
- 多窗口管理，能够自由打开多个窗口执行执行工作
- 多项 UI 自定义选项

## 安装

下载 [Releases](https://github.com/NLR-DevTeam/NLR-Shell/releases) 中最新的版本即可运行

## 命令行启动参数

如果不带任何参数启动，程序会直接进入图形主界面。如果希望在脚本或终端中快速直达远程会话，可使用 `-connect`：

```sh
NLRShell -connect [user@]host[:port] [-identity 私钥文件] [-password 密码] [-passphrase 私钥口令]
```

- 该模式专为免交互设计：会自动接受并信任主机密钥
- 如果缺少认证凭据，命令将直接退出并报错

## 源码构建

### Windows

推荐使用 PowerShell 运行随附脚本：

```powershell
.\build.ps1            # 编译 Release 版本
.\build.ps1 -Test      # 编译前执行单元测试
.\build.ps1 -Console   # 保留控制台窗口输出（便于排查故障）
```

### Linux

项目基于 Gio UI 开发。由于上游 Gio 目前在 Linux 下存在问题，仓库内附带了针对该问题的补丁（位于 `tools/giopatch`）。

**建议优先使用提供的构建脚本编译**。直接执行 `go build` 虽然也能成功，但会回退到未经打补丁的依赖，Linux 下将保留系统原生标题栏。

构建依赖 cgo 以及 X11 / Wayland 的系统开发库。请先安装编译依赖：

- **Arch Linux**:
  ```sh
  sudo pacman -S base-devel libx11 libxkbcommon libxkbcommon-x11 \
                 libxcursor libxfixes wayland libglvnd mesa
  ```
- **Debian / Ubuntu**:
  ```sh
  sudo apt install build-essential pkg-config libx11-dev \
                   libx11-xcb-dev libxcursor-dev libxfixes-dev libxkbcommon-dev \
                   libxkbcommon-x11-dev libwayland-dev libegl-dev libgles-dev
  ```

依赖就绪后运行：

```sh
./build.sh             # 编译产物为当前目录下的 ./NLRShell
./build.sh --test      # 编译前执行测试
```

## Linux 平台注意事项

- **配置路径**：配置文件存放在 `$XDG_CONFIG_HOME/nlrshell`（默认即 `~/.config/nlrshell`）。如果你需要便携模式，可以指定环境变量 `NLRSHELL_DATA` 来自定义路径（Windows 下路径默认为 `%APPDATA%\NLR Shell`）。
- **凭据安全**：Windows 下密码和密钥口令直接托管给 DPAPI；Linux 环境下，密码会保存在配置目录的 `secret.key` 中（文件权限自动设为 `0600`），并通过 XChaCha20-Poly1305 加密保存，请妥善保管该密钥文件。
- **SSH Agent**：Linux 下依赖 `SSH_AUTH_SOCK` 环境变量与系统的 `ssh-agent` 通信。
- **桌面环境兼容性**：目前界面在 KDE Plasma 环境下测试最为充分；在其他桌面环境（如 GNOME、XFCE 或各类平铺式 WM）下自绘窗体可能会有渲染细节差异。
- 如果缺少 Emoji字体请确保系统安装了Emoji字体：

  ```sh
  sudo apt install fonts-noto-color-emoji   # Debian / Ubuntu
  sudo pacman -S noto-fonts-emoji           # Arch Linux
  ```

## 支持

欢迎[提交问题/建议](https://github.com/NLR-DevTeam/NLR-Shell/issues)，[进群讨论](https://qm.qq.com/cgi-bin/qm/qr?k=3fydvbI64F7r0Tz2Y5BTWfJi-irnBnSz&authKey=ib%2FY0l5RwzWu2X5cDRK%2FB2swvZotR7f68BpJWLy5TuT1vRQGjya%2FT36dgV1xn4fs&noverify=0&group_code=182850795)
