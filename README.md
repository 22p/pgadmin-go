# GoPgAdmin

用 Go 写的轻量级 PostgreSQL Web 管理台。

![登录](docs/login.png)

![数据浏览](docs/dashboard.png)

## 功能

**连接与认证**

| 认证方式 | 说明 |
| --- | --- |
| 密码 | SCRAM-SHA-256 / MD5,由服务端决定;支持 SSL(disable / require / verify-ca / verify-full) |
| 免密 | trust / peer 等无密码场景;勾选 Unix socket 可走本地套接字 |
| 客户端证书 | mTLS:上传 CA(可选)、客户端证书与私钥,仅驻留内存;强制 TLS,不允许回退明文 |
| 连接串 | 直接粘贴 `postgres://...` 或 `key=value` 形式的 DSN,便于接入云数据库 |

**日常管理**

- 对象树:数据库 → schema → 表 / 视图,支持按名称过滤
- 数据浏览:分页、每页 25–200、列头排序;多条件筛选(=、≠、>、≥、<、≤、包含、开头是、LIKE、为空 / 不为空),可隐藏列并随分页/排序保持
- 行内编辑:按主键定位,支持新增、修改、删除;可空字段留空写 NULL
- SQL 编辑器:多语句执行、逐条结果集、命令标签与耗时(Ctrl/⌘ + Enter)
- SQL 历史(最近 30 条)与收藏(当前会话),结果一键导出 CSV / JSON
- 结构页:列 / 类型 / 可空 / 默认值 / 主键、索引、外键、表大小
- 建表 / 改表:可视化定义列、主键、默认值;可追加或删除列
- 运维面板:活动会话、锁与阻塞、连接数、缓存命中率、表大小 Top 10,支持取消查询、终止连接、ANALYZE / VACUUM
- 备份恢复:pg_dump 三种格式(SQL / custom / tar)、按表导出、上传恢复(psql 或 pg_restore)
- ER 图:按 schema 生成表结构与外键关系图
- 只读模式:数据库层只读事务 + 隐藏所有写入入口

**界面**

- 浅色 / 深色主题(跟随系统,可手动切换)、响应式布局、移动端抽屉侧栏
- 原生 HTML / CSS / JS,通过 `go:embed` 打包,无 npm、无外部字体与脚本

## 快速开始

```sh
cd /root/pgadmin-go
go build -o pggo .
./pggo -addr 127.0.0.1:8080
```

打开 http://127.0.0.1:8080,用数据库账号登录即可。默认只监听本机。

## Docker

```sh
docker build -t pggo:local .
docker run --rm -p 8080:8080 pggo:local

# 免登录直接进入(推荐配合反向代理鉴权)
docker run --rm -p 8080:8080 \
  -e PGGO_HOST=host.docker.internal \
  -e PGGO_USER=pgadmin \
  -e PGGO_PASSWORD=pgadmin \
  -e PGGO_DB=demo \
  -e PGGO_SSLMODE=disable \
  pggo:local
```

镜像基于 Alpine,以非 root 用户 `app` 运行,内置 `postgresql-client`(备份页需要)、`/login` 健康检查,监听 `0.0.0.0:8080`。

## 环境变量自动登录

配置凭据后,访问首页直接进入管理台,不再显示登录页;顶栏会标注「自动登录」并隐藏退出按钮。

| 变量 | 说明 |
| --- | --- |
| `PGGO_DSN` | 完整连接串,优先级最高,设置后忽略下面的分项 |
| `PGGO_HOST` / `PGGO_PORT` | 主机与端口,默认 `127.0.0.1:5432` |
| `PGGO_USER` | 用户名(设置该变量即启用自动登录) |
| `PGGO_PASSWORD` | 密码;免密认证时留空 |
| `PGGO_DB` | 默认数据库,默认 `postgres` |
| `PGGO_SSLMODE` | disable / require / verify-ca / verify-full |
| `PGGO_AUTH` | password(默认)/ trust / cert |
| `PGGO_SOCKET` | `1` 时走 Unix socket |
| `PGGO_SSLROOT` / `PGGO_SSLCERT` / `PGGO_SSLKEY` | cert 认证的 CA / 证书 / 私钥文件路径 |
| `PGGO_READONLY` | `1` 开启只读模式,等价于 `-readonly` |

```sh
PGGO_HOST=127.0.0.1 PGGO_USER=pgadmin PGGO_PASSWORD=pgadmin PGGO_DB=demo ./pggo
PGGO_DSN='postgres://user:pass@db.internal:5432/app?sslmode=verify-full' ./pggo
```

自动登录使用进程内共享会话:连接失败时页面返回 502 并给出原因(每 5 秒重试一次)。

> 开启后,任何能访问该端口的人都会获得该数据库账号的权限。请只在本机监听,或放在做足鉴权的反向代理之后。

## 只读模式

```sh
./pggo -readonly            # 或 PGGO_READONLY=1
```

- 每个连接设置 `default_transaction_read_only=on`,写语句由数据库直接拒绝
- 界面隐藏建库/删库、清空/删除表、新增/编辑/删除行、建表改表、恢复与运维操作按钮
- 服务端对写接口额外返回 403,避免直接构造请求绕过界面

## 数据编辑

- 编辑依赖主键:没有主键的表只开放 SQL 编辑器,避免误改多行
- 新增行:留空的字段不写入,由数据库默认值处理
- 修改行:可空字段留空写 NULL;主键字段只读
- 删除行:需二次确认,按主键精确删除

## 备份与恢复

- 依赖 `pg_dump` / `pg_restore` / `psql`(本机已安装,Fedora/Alpine 包为 `postgresql-client`)
- 导出支持 SQL 文本、自定义归档、tar;可选仅结构、单表导出;文件名带时间戳
- 恢复上限 64MB:SQL 文本走 `psql -v ON_ERROR_STOP=1`,归档走 `pg_restore`;可勾选 `--clean --if-exists`
- 客户端证书认证暂不支持命令行备份(证书不落盘),请使用密码或 DSN 登录后进行

## 认证方式细节

### 客户端证书(mTLS)

服务端至少需要开启 TLS 并要求客户端证书,例如 `postgresql.conf`:

```conf
ssl = on
ssl_ca_file = '/etc/postgresql/ca.crt'
ssl_cert_file = '/etc/postgresql/server.crt'
ssl_key_file = '/etc/postgresql/server.key'
```

`pg_hba.conf`:

```conf
hostssl all all 0.0.0.0/0 cert clientcert=verify-full
```

在登录页选择「客户端证书」,上传客户端证书与私钥(自签 CA 时同时上传 CA),SSL 模式选择
`verify-full` 或 `require`。证书只在进程内存中使用,不写入磁盘。

### 连接串

```text
postgres://user:pass@db.example.com:5432/app?sslmode=verify-full
host=db.example.com port=5432 user=app dbname=app sslmode=require
```

登录后仍可在左侧切换其他数据库,连接池会按库自动创建。

## 配置

| 参数 | 默认值 | 说明 |
| --- | --- | --- |
| `-addr` | `127.0.0.1:8080` | HTTP 监听地址;容器内使用 `:8080` |
| `-readonly` | false | 只读模式 |
| `-version` | - | 打印版本号并退出 |

## GitHub Actions 流水线

与 moyue 项目一致的分层构建方式:

- `.github/workflows/ci.yml`
  - push 到 main / PR / 手动触发
  - `gofmt` 检查、`go vet`、`go test -race`(附带 PostgreSQL 18 service,集成测试直连)
  - 多平台二进制构建:linux/darwin 的 amd64+arm64、windows amd64,产物上传 artifact
  - Docker 多架构(linux/amd64,linux/arm64)构建验证,不推送
- `.github/workflows/release.yml`
  - 推送 `v*` tag 或手动指定版本时触发
  - 测试 → 多平台二进制 → 多架构镜像推送到 `ghcr.io/<owner>/pggo:latest` 与 `:vX.Y.Z`
  - 自动创建 GitHub Release 并附带各平台压缩包

无需额外 secrets,GHCR 使用自动注入的 `GITHUB_TOKEN`(`packages: write`)。

## 开发与测试

```sh
make test            # 单元测试(含模板渲染回归、筛选与建表 DDL)
make vet
make fmt
make test-integration PGTEST_DSN='postgres://user:pass@127.0.0.1:5432/postgres'
```

集成测试覆盖连接池、多语句执行、库列表、对象树与浏览页筛选;未设置 `PGTEST_DSN` 时自动跳过。

## 安全须知

- 登录凭据与客户端证书仅保存在进程内存;会话 12 小时过期,重启即失效(单实例内存会话)
- 所有 POST 校验 CSRF 令牌;Cookie 为 `HttpOnly` + `SameSite=Lax`
- SQL 条件值以字面量转义,标识符统一 `pgx.Identifier` 转义,排序/筛选列白名单校验
- 工具拥有所用数据库账号的全部权限:请使用最小权限账号、限制监听地址,并在反向代理后启用 HTTPS
- 密码 / 证书不会写入日志

## 已知限制

- 数据编辑要求表有主键;批量更新请使用 SQL 编辑器
- SQL 历史与收藏保存在会话内存,重启或换浏览器会丢失
- 备份恢复为同步操作,超大库建议直接用 `pg_dump`;上传恢复上限 64MB
- ER 图最多展示 24 张表,不包含视图与跨 schema 关系
- 无用户与权限管理界面;会话存于单实例内存,横向扩容需外部会话存储
