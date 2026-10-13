1. 文档里提到的 website-api 项目，代码在 /Users/antony/Workspace/mangox/mangox-api, 可以直接读取
2. 文档里提到的 admin-api 项目，代码在 /Users/antony/Workspace/mangox/mangox-blockmatrix, 它对应的前端项目代码在 /Users/antony/Workspace/mangox/blockmatrix-web，都可以直接读取
3. MVP 阶段不实现业务 Audit、OpenTelemetry、Metric 等具体代码，可以生成预留的模板代码和注释说明
4. MVP 阶段不实现 ai-copilot-web、ai-copilot-api 的登录功能, internal-mcp-server 调用 admin-api HTTP 接口时，可以将 bearer_token, cookie, xsrf_token 等配置到配置文件
5. MVP 阶段 internal-mcp-server 只实现 admin-api 单个 adapter 的部分接口, 暂不考虑 website-api 这个 adapter

## Git Submodules

本仓库将以下目录作为 git submodules 管理:

- `ai-copilot-api`
- `ai-copilot-web`
- `internal-mcp-server`

### Clone

克隆 umbrella 仓库时同时初始化并拉取所有 submodules:

```bash
git clone --recurse-submodules <umbrella-repo-url>
```

如果已经克隆过 umbrella 仓库:

```bash
git submodule update --init --recursive
```

### Daily Workflow

拉取 umbrella 仓库最新提交, 并让所有 submodules 移动到父仓库记录的 commit:

```bash
git pull
git submodule update --init --recursive
```

查看 submodules 当前 commit 和状态:

```bash
git submodule status
git submodule foreach 'git status --short --branch'
```

在指定 submodule 中开发:

```bash
cd ai-copilot-api
git switch main
git pull --ff-only
```

submodule 内的代码提交并推送到它自己的远端后, 回到 umbrella 仓库记录新的 submodule commit:

```bash
git add ai-copilot-api
git commit -m "Update ai-copilot-api"
git push
```

### Maintenance

把所有 submodules 更新到各自远端默认分支的最新 commit:

```bash
git submodule update --remote
git add ai-copilot-api ai-copilot-web internal-mcp-server
git commit -m "Update submodules"
```

`.gitmodules` 中的 URL 变更后, 同步到本地 submodule 配置:

```bash
git submodule sync --recursive
```

添加新的 submodule:

```bash
git submodule add ../<repo-name>.git <path>
git commit -m "Add <path> submodule"
```


