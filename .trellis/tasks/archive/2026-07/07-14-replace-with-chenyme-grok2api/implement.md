# 执行计划

1. 记录替换前状态
   - 获取当前 commit、remote、Git 状态、Docker compose 状态和磁盘空间。
   - 固化目标仓库 `main` 的 commit SHA。
   - 确认账号复检报告总数为 606，分类为 334/95/177。

2. 停止旧服务并建立备份
   - 执行旧项目 `docker compose stop`，确认容器停止。
   - 创建 `/root/grok2api-backups/<timestamp>/`，权限设为 `700`。
   - 备份完整仓库、`.git`、`.env`、`data/`、`logs/` 和运行状态信息。
   - 生成备份文件清单与 SHA-256 校验和，并抽样验证可读取性。
   - 回退点：备份未验证前不得删除或覆盖当前业务文件。

3. 生成账号迁移材料
   - 从备份的 `accounts.db` 读取账号。
   - 以复检报告 SHA-256 为过滤条件，排除 177 个永久失效账号。
   - 生成包含 429 个账号的目标 Web 导入 JSON，权限设为 `600`。
   - 验证无重复 Token、数量为 429，且文件位于仓库外。

4. 暂存并核验目标仓库
   - 克隆 `https://github.com/chenyme/grok2api.git` 到仓库外暂存目录。
   - 校验 `origin/main`、默认分支、目标 commit 和关键文件。
   - 在替换前运行能独立完成的目标测试；失败则停止替换并报告。

5. 替换工作树与 Git 元数据
   - 暂存保留 `.agents/`、`.claude/`、`.codex/`、`.trellis/`、`AGENTS.md`。
   - 将目标仓库业务文件和 `.git` 同步到 `/root/grok2api`，删除旧业务残留。
   - 恢复保留的管理目录和 `AGENTS.md`。
   - 验证 `origin`、HEAD、旧路径不存在、目标路径存在。
   - 回退点：任何路径或 Git 校验失败时立即从备份恢复。

6. 生成目标配置
   - 从 `config.example.yaml` 创建 `config.yaml`。
   - 生成 JWT Secret、凭据加密密钥和管理员密码。
   - 写入仓库外 `new-project-credentials.txt`，权限 `600`；不输出密钥值。
   - 保持默认 SQLite、memory runtime store、端口 8000 和 Asia/Shanghai 时区。
   - 执行 `docker compose config` 验证配置。

7. 执行目标质量检查
   - Backend：使用 Go 1.26 环境运行 `go test ./...`，单次测试命令限制 60 秒；随后运行 `go vet ./...`。
   - Frontend：使用 pnpm 11.5.2 运行 `pnpm install --frozen-lockfile`、`pnpm lint`、`pnpm build`。
   - 运行 `docker build` 或拉取并核验目标官方镜像。
   - 任何失败保留原始输出，不使用模拟成功或跳过错误。

8. 启动并验证新服务
   - 执行 `docker compose up -d`，等待容器健康。
   - 验证端口 8000、健康接口和管理端页面。
   - 使用生成的管理员凭据登录，确认 Bearer Token 可用。

9. 导入并核验账号
   - multipart 上传仓库外的 429 账号 JSON 到 `/api/admin/v1/accounts/web/import`。
   - 保存 SSE 导入结果，核对 created/updated/syncFailed。
   - 查询账号汇总，确认目标数据库中账号总数为 429；暂时限流或同步失败不误删。

10. 最终检查与交付
   - 运行 `git status --short --branch`，确认只存在预期的管理目录和本地配置。
   - 确认备份目录、凭据文件权限、目标 remote/HEAD、容器状态和账号数量。
   - 记录所有验证命令及结果；未执行项明确标记 `没验证`。
   - 保留旧备份和新 named volume，不做不可逆清理。
