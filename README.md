# RunCmd

**RunCmd** 是一个运行在群晖 NAS（DSM 7）上的 Shell 命令 Web 管理工具，以群晖套件（`.spk`）形式安装。通过浏览器即可管理、参数化运行预设 Shell 命令，无需 SSH 登录。

---

## 功能特性

- **参数化命令模板**：支持 `{0}` / `{1}` 两个占位符，运行时从下拉列表单选替换，防止手动输入错误
- **TOTP 两步验证**：基于 RFC 6238 / RFC 4226，兼容 Google Authenticator、Aegis 等标准验证器 App
- **RSA Token 鉴权**：登录后签发内存 RSA 私钥签名的 Token，重启自动失效；支持「记住登录」（365 天 localStorage）和会话登录（1 天 sessionStorage）
- **WoL 网络唤醒**：内置 `wol` 二进制，可直接在命令模板中调用
- **SSH 免密登录**：安装时自动生成 RSA 4096 密钥对，公钥可在页面一键查看并分发到目标主机
- **文件服务接口**：将文件放到 `var/return/` 目录，通过 `/api/file?file=name` 无鉴权直接访问，支持 JSON、图片、视频（Range 请求）等任意格式
- **主机 IP 上报**：客户端可免鉴权上报 hostname 与一个或多个局域网 IP，服务端自动记录访问来源的外网 IP
- **主机状态页面**：点击首页「主机」查看主机名、外网 IP 和局域网 IP，按最近上报时间倒序排列
- **SQLite 持久化**：命令数据存储在 `/var/packages/runcmd/var/runcmd.db`，升级不丢失
- **零依赖部署**：Go 单二进制 + 内嵌 HTML/Vue 前端，无需公网 CDN、CGO 或其他运行时依赖

---

## 技术栈

| 层 | 技术 |
|---|---|
| 后端 | Go 1.21+，`net/http`，`modernc.org/sqlite`（纯 Go SQLite） |
| 前端 | Vue 3.5.40（本地内嵌，无构建步骤），原生 CSS |
| 认证 | TOTP（RFC 4226/6238 手动实现），RSA-PKCS1v15 签名 Token |
| 打包 | Synology SPK 格式，DSM 7.0+ |

---

## 项目结构

```
runcmd/
├── main.go               # HTTP 服务、路由、命令 CRUD
├── auth.go               # TOTP、RSA Token、鉴权中间件
├── static/
│   ├── index.html        # Vue 3 单页前端（内嵌）
│   └── vue.global.prod.js # Vue 3 生产版运行时（内嵌，不依赖 unpkg）
└── synopkg/
    ├── INFO              # 套件元数据
    ├── build.sh          # 打包脚本
    ├── conf/
    │   ├── privilege     # 运行用户配置
    │   └── resource      # DSM 资源声明
    ├── scripts/
    │   ├── postinst      # 安装后：生成 SSH 密钥、创建 return/ 和 ip/ 目录
    │   ├── preinst       # 安装前：创建 runcmd 用户
    │   ├── preupgrade    # 升级前钩子（DSM 升级校验必需）
    │   ├── postupgrade   # 升级后钩子（DSM 升级校验必需）
    │   ├── preuninst     # 卸载前：停止进程
    │   └── start-stop-status
    ├── nginx/
    │   └── dsm.runcmd.conf  # nginx 反代配置（手动部署）
    └── ui/
        ├── config        # DSM 套件中心 UI 配置
        └── images/       # 各尺寸应用图标
```

---

## 构建与安装

### 前置要求

- Go 1.21+
- 项目根目录下存在编译好的 `wol` 二进制（linux/amd64）

### 1. 编译 Go 二进制

```bash
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o runcmd_linux .
```

### 2. 打包 SPK

```bash
cd synopkg
bash build.sh
# 输出：synopkg/dist/runcmd-1.0.0-0004.spk（以 INFO 中 version 为准）
```

### 3. 安装到群晖

套件中心 → **手动安装** → 上传 `.spk` 文件。

安装完成后服务监听 `0.0.0.0:38083`。

从已安装版本直接升级时，新包的 `INFO` 版本号必须高于当前版本，并且必须包含可执行的 `scripts/preupgrade` 和 `scripts/postupgrade`。当前构建版本为 `1.0.0-0004`；`build.sh` 会直接读取 `INFO` 中的版本号作为输出文件名，发布新版本时只需修改 `INFO`。

---

## 首次使用

### 配置 TOTP 密钥

安装完成后服务启动，此时进入 **10 分钟初始化窗口期**：

1. 打开应用，首次访问会跳转到「初始配置」页面
2. 在 Google Authenticator / Aegis 等 App 中选择 **手动输入密钥**
3. 输入账户名（如 `runcmd@nas`），粘贴 Base32 格式密钥，时间步长保持 30 秒
4. 将同一 Base32 密钥填入页面输入框，点击 **保存密钥**
5. 跳转到登录页，输入验证器显示的 6 位码登录

> **10 分钟未配置的情况**：服务会自动生成一个随机 TOTP 密钥写入本地文件，封闭初始化窗口期。此时需要 SSH 登录 NAS 获取密钥并手动录入验证器 App：
> ```bash
> cat /var/packages/runcmd/var/totp_secret
> ```
> 随后在验证器 App 中手动添加，使用该 Base32 字符串作为密钥。

> **密钥存储位置**：`/var/packages/runcmd/var/totp_secret`（0600 权限，仅 root 可读）

### 更新密钥

登录后点击顶部工具栏的 **🔐 密钥** 按钮，重新输入 Base32 密钥即可更新。

---

## nginx 反代配置（推荐）

配置后可通过群晖当前访问端口（5000/5001 等）访问，点击套件图标自动跳转到正确端口和路径。

**方法**（以 root 或管理员 SSH 执行）：

```bash
cp /volume1/@appstore/runcmd/nginx/dsm.runcmd.conf /etc/nginx/conf.d/dsm.runcmd.conf
nginx -s reload
```

配置文件内容：

```nginx
location /webman/3rdparty/runcmd/ {
    proxy_pass         http://127.0.0.1:38083/;
    proxy_set_header   Host $http_host;
    proxy_set_header   X-Real-IP $remote_addr;
    proxy_set_header   X-Real-HTTPS $https;
    proxy_set_header   X-Server-Port $server_port;
    proxy_http_version 1.1;
}
```

> ⚠️ 群晖 DSM 升级或套件重新安装后，`/etc/nginx/conf.d/` 内的自定义配置可能被清除，需重新执行 `cp` 命令。

---

## API 接口

需要鉴权的接口通过 `X-Token` 请求头携带 Token。认证接口、文件服务和主机上报/列表接口无需鉴权。

### 认证

| 方法 | 路径 | 鉴权 | 说明 |
|---|---|---|---|
| GET | `/api/auth/status` | 否 | 返回 `{"has_secret": bool}` |
| POST | `/api/auth/login` | 否 | 验证 TOTP，返回 `{"token": "..."}` |
| POST | `/api/auth/secret` | 条件¹ | 设置/更新 TOTP Base32 密钥 |

¹ 首次配置（无密钥时）不需要鉴权；已有密钥时需要。

### 命令管理

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/api/commands` | 获取全部命令列表 |
| POST | `/api/commands` | 创建命令 |
| PUT | `/api/commands/:id` | 更新命令 |
| DELETE | `/api/commands/:id` | 删除命令（限速 2s） |
| POST | `/api/commands/:id/run` | 执行命令（限速 2s） |

### 其他

| 方法 | 路径 | 鉴权 | 说明 |
|---|---|---|---|
| GET | `/api/pubkey` | 是 | 获取 SSH RSA 公钥内容 |
| GET | `/api/file?file=文件名` | 否 | 服务 `var/return/` 目录中的文件 |
| GET | `/api/host/report?hostname=主机名&local_ip=IP1,IP2` | 否 | 上报主机名、局域网 IP，并自动获取客户端外网 IP |
| GET | `/api/hosts` | 否 | 返回所有主机记录文件名，按修改时间倒序排列 |

Vue 运行时由 Go 二进制内嵌并通过无扩展名路径 `/vue-runtime` 提供，响应类型仍为 `text/javascript`。群晖 DSM nginx 会优先拦截 `/webman/3rdparty/.../*.js` 和 `static/` 静态资源路径并返回自身的 404，使用无扩展名路由可确保请求进入套件的 Go 反向代理。

### 主机 IP 上报

上报示例：

```bash
curl "http://nas:38083/api/host/report?hostname=office-nas&local_ip=192.168.1.10,10.0.0.10"
```

成功响应：

```json
{
  "ok": true,
  "filename": "office-nas-203.0.113.10-192.168.1.10-10.0.0.10"
}
```

处理规则：

- `hostname` 必填，遵循标准 hostname 标签规则，只允许字母、数字、连字符和点；禁止路径字符、连续点、空标签及超过长度限制的标签。
- `local_ip` 必填，多个 IP 使用英文逗号分隔；支持 IPv4/IPv6、自动去重，单次最多 32 个。
- 外网 IP 优先从群晖反向代理传递的 `X-Forwarded-For` 获取，其次读取 `X-Real-IP` 和 TCP 客户端地址；所有候选值均需通过 IP 格式校验。
- 记录保存在 `/var/packages/runcmd/var/ip/`，文件名格式为 `hostname-外网IP-局域网IP1-局域网IP2...`。
- 每次上报先删除相同 `hostname-` 开头的旧文件，再创建或刷新当前文件，确保同一 hostname 只保留一条记录。
- 每次成功上报后自动删除修改时间超过 30 天的记录。
- 写入及列表读取使用互斥锁保护，避免并发上报产生同 hostname 的重复文件。

获取列表：

```bash
curl http://nas:38083/api/hosts
```

响应为按修改时间从新到旧排列的文件名数组。登录页面后也可点击顶部「主机」按钮，以“主机 / 外网 IP / 局域网 IP”三列查看。

---

## 命令模板说明

### 占位符规则

| 占位符 | 替换来源 | 数量 |
|---|---|---|
| `{0}` `{1}` `{2}` … | 运行时从「列表1」单选一行，行内按 `\|` 分割后依次替换 | 无限 |
| `%0` `%1` `%2` … | 运行时从「列表2」单选一行，行内按 `\|` 分割后依次替换 | 无限 |

- 同一占位符在模板中出现多次，**全部替换**
- 每行尾部 `#` 为注释，运行时整行先去掉 `#` 及其后内容，再按 `|` 拆分

### 列表格式

```
# 单值行（只替换 {0} 或 %0）
192.168.1.100  # 客厅 PC

# 多值行（用 | 分隔，依次替换 {0} {1} {2}… 或 %0 %1 %2…）
192.168.1.100|22|root  # 生产服务器
192.168.1.101|8022|admin  # 测试服务器
```

### 示例

**WoL 唤醒**（单值，列表1 → `{0}`）：

```
# 命令模板
/var/packages/runcmd/var/wol {0}

# 列表1（每行一个 MAC 地址）
AA:BB:CC:DD:EE:FF  # 客厅 PC
11:22:33:44:55:66  # 卧室 NUC
```

**SSH 多参数执行**（列表1 提供主机+端口+用户，列表2 提供命令）：

```
# 命令模板
ssh -i /var/packages/runcmd/var/.ssh/id_rsa \
    -o StrictHostKeyChecking=no \
    -p {1} {2}@{0} '%0'

# 列表1（行内 | 分隔：IP | 端口 | 用户）
192.168.1.100|22|root    # 生产服务器
192.168.1.101|8022|admin # 测试服务器

# 列表2（要执行的命令，单值替换 %0）
uptime
df -h
free -m
```

**同一占位符多次出现**：

```
# 命令模板（{0} 出现两次，全部替换）
echo "Host: {0}" && ping -c 3 {0}

# 列表1
192.168.1.1  # 网关
8.8.8.8      # Google DNS
```

---

## 鉴权机制说明

```
初始化保护：
  服务启动 → 检测 totp_secret 是否存在
    → 不存在：记录日志，启动 10 分钟倒计时
         → 10 分钟内用户完成配置：正常使用
         → 10 分钟后仍未配置：自动生成 160-bit 随机密钥写入文件，
           窗口关闭，需 SSH 获取密钥后录入验证器 App

登录流程：
  输入 6 位 TOTP 码
    → 全局限速（2s 内仅允许一次尝试，所有 IP 共享）
    → 后端验证（当前步长 + 前一步长，共 60s 窗口）
    → 签发 Token：base64url(JSON) . base64url(RSA-PKCS1v15-SHA256 签名)
    → 前端存储（localStorage 365天 或 sessionStorage 1天）

请求流程：
  携带 X-Token 头
    → 后端验证签名 + 检查时间（±10 分钟容差）
    → 验证通过：延签新 Token 写入 X-Token 响应头
    → 前端更新存储的 Token

重启影响：
  RSA 密钥对仅存内存，服务重启后所有已颁发 Token 立即失效，需重新登录。

setuid 防护：
  每次服务启动前，start-stop-status 脚本自动执行 chmod u-s
  移除二进制的 setuid root 位，确保服务以 runcmd 用户身份运行。
```

---

## 数据目录

| 路径 | 权限 | 内容 |
|---|---|---|
| `/var/packages/runcmd/var/runcmd.db` | 640 | SQLite 数据库（命令数据） |
| `/var/packages/runcmd/var/totp_secret` | 600 | TOTP Base32 密钥 |
| `/var/packages/runcmd/var/return/` | 750 | 文件服务目录，放入后可通过 `/api/file?file=` 访问 |
| `/var/packages/runcmd/var/ip/` | 750 | 主机 IP 上报记录；安装/升级时自动创建，超过 30 天的记录自动清理 |
| `/var/packages/runcmd/var/.ssh/id_rsa` | 600 | SSH RSA 私钥 |
| `/var/packages/runcmd/var/.ssh/id_rsa.pub` | 644 | SSH RSA 公钥 |
| `/var/packages/runcmd/var/wol` | 755 | WoL 工具二进制 |
| `/var/packages/runcmd/var/runcmd.pid` | — | 进程 PID 文件 |
| `/var/packages/runcmd/var/runcmd.log` | — | 服务日志 |

### 文件服务使用说明

将文件放到 `return/` 目录，无需重启即可访问：

```bash
# 复制文件到服务目录
cp /path/to/data.json /var/packages/runcmd/var/return/data.json

# 通过接口访问（无需登录）
curl http://nas:38083/api/file?file=data.json
# 或通过 nginx 反代
curl http://nas:5000/webman/3rdparty/runcmd/api/file?file=data.json
```

支持的场景：
- **JSON / 文本**：直接返回内容，浏览器可直接查看
- **图片**（jpg / png / gif / webp）：浏览器内联显示
- **视频**（mp4 / webm）：支持 `Range` 请求，可在 `<video>` 标签中流式播放
- **其他二进制**：触发浏览器下载

安全限制：文件名不得含路径分隔符（`/` `\`）或 `..`，仅允许访问 `return/` 目录内的直接文件。

---

## License

MIT
