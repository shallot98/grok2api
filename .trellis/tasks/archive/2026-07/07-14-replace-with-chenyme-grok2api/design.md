# 技术设计

## 替换边界

当前仓库与目标仓库没有共同 Git 祖先，因此不做 merge、rebase 或 `--allow-unrelated-histories`。采用“仓库外备份 + 目标仓库暂存克隆 + 受控同步”的方式替换业务源码和 `.git`，同时显式保留 `.agents/`、`.claude/`、`.codex/`、`.trellis/`、`AGENTS.md`。

替换过程中不保留旧 `app/`、`docs/`、`scripts/`、Python 依赖文件、旧 compose 文件或 `.venv/`，避免两个实现混合运行。旧 `.env`、`data/`、`logs/` 只在备份目录保留，不直接复制到新项目。

## 备份与回退

替换前停止旧 compose 服务，确保 SQLite 和日志处于一致状态。备份目录位于仓库外 `/root/grok2api-backups/<timestamp>/`，权限为 `700`，包含：

- 当前完整 Git 仓库与工作树快照；
- `.env`、`data/`、`logs/`；
- 当前 commit、remote、容器和镜像信息；
- 文件清单与校验和；
- 账号复检报告及后续迁移文件。

回退时停止新容器，移走新工作树，将备份恢复到 `/root/grok2api`，再使用旧 compose 启动。新项目 named volume 不在确认成功前删除，避免二次数据丢失。

## 配置与密钥

从目标仓库 `config.example.yaml` 生成被 Git 忽略的 `config.yaml`。自动生成：

- 至少 32 字符的 `jwtSecret`；
- Base64 编码 32 字节的 `credentialEncryptionKey`；
- 强随机管理员初始密码。

完整凭据同时保存到备份目录的 `new-project-credentials.txt`，权限为 `600`，不在控制台输出值。默认保持端口 `8000`、SQLite、memory runtime store；旧 `.env` 中可兼容的端口和时区仅按字段映射，不整文件复用。

## 账号迁移

旧 `accounts.db` 不直接复用。根据 `account-validation.json` 中 SHA-256 判定，从备份数据库导出 429 个非永久失效 SSO Token，生成权限为 `600` 的 JSON 导入文件：

```json
{"provider":"grok_web","accounts":[{"name":"...","sso_token":"...","tier":"auto"}]}
```

177 个 `permanently_invalid` 账号不导入。334 个实时可用和 95 个暂时限流账号导入，目标项目负责重新同步账号状态和额度；旧额度、使用次数、历史状态不迁移。

新服务启动后通过 `/api/admin/v1/auth/login` 获取短期 Bearer Token，再以 multipart 请求调用 `/api/admin/v1/accounts/web/import`。导入文件始终留在仓库外，导入完成后核对创建/更新数量和账号总数。

## 验证策略

静态验证目标 HEAD、origin、旧业务路径清除和管理目录保留。质量验证对齐目标 CI：backend `go test ./...`、`go vet ./...`，frontend `pnpm install --frozen-lockfile`、`pnpm lint`、`pnpm build`。本机 Go 配置损坏，因此后端优先使用官方 Go 1.26 Docker 镜像执行；最终还需启动目标官方镜像，验证健康检查、管理员登录和 429 个账号导入结果。

## 风险控制

- 替换期间存在停机时间，从停止旧容器到新服务健康检查通过为止。
- 429 只代表暂时限流，保留迁移；仅明确无效凭据响应才排除。
- `credentialEncryptionKey` 一旦写入账号后不得更换，凭据文件必须长期保留。
- 若目标构建、启动、登录或导入失败，不修改旧备份；报告实际错误并按回退步骤恢复旧服务。
