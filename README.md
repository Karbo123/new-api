<div align="center">

# New API · 本地补丁版（Karbo123 fork）

**本仓库 fork 自官方 [QuantumNous/new-api](https://github.com/QuantumNous/new-api)（[查看官方最新代码](https://github.com/QuantumNous/new-api/tree/main)），仅叠加了少量本地修复，详见下文。**

### ⬇️ 下载 patched Windows exe

**永久直链（总是最新 main 构建）：**

> <https://github.com/Karbo123/new-api/releases/latest/download/new-api-patched-windows.exe>

**Release 页面**（含 sha256 校验和）：[releases/tag/patched-latest](https://github.com/Karbo123/new-api/releases/tag/patched-latest)

</div>

---

## 我们相对官方改了什么

本仓库 main 分支 = **官方 main 最新提交 + 1 个补丁提交**。共包含两个修复：

---

### 修复 1：DeepSeek V4 thinking 模式多轮对话 400 报错

**涉及文件**：`relaykit/relayconvert/internal/oai_responses/to_oai_chat_req.go`（+ 新增单元测试 `to_oai_chat_req_reasoning_test.go`）

- **问题现象**：客户端通过 `/v1/responses`（OpenAI Responses 协议）访问 OpenAI 兼容通道（如 OpenCode Zen，`https://opencode.ai/zen/go`）上的 `deepseek-v4-flash`，第一轮对话正常，第二轮及之后必然报错：

  ```
  400 invalid_request_error: The `reasoning_text` in the thinking mode must be passed back to the API.
  ```

- **根因分析**：DeepSeek V4 thinking 模式（经 OpenCode Zen 网关）要求多轮对话时，请求历史里**每条 assistant 消息都必须携带 `reasoning_content` 字段**（把上一轮的推理内容传回去）。在标准 Responses 协议里，推理内容是独立的 `{"type":"reasoning"}` 输入项（与 assistant 消息平级）；客户端（如 Console Go / opencode）会原样回传这些 reasoning 项。而 new-api 的 **Responses→Chat Completions 请求转换器**不认识 `type:"reasoning"` 项，处理时产生两个问题：
  1. reasoning 项被当作一条**空 user 消息**塞进历史（污染上下文）；
  2. 它携带的推理文本被完全丢弃，assistant 历史消息没有 `reasoning_content` —— 上游校验失败，返回 400。

- **修复方案**（`responsesRequestMessagesToChat` 转换逻辑）：
  - 遇到 `type:"reasoning"` 输入项时，从其 `content`（raw 推理文本，OpenAI `reasoning_text` 形态）、`summary`（摘要形态）、`reasoning_details`（OpenRouter 形态，仅取 `reasoning.text` 类型）三个字段中按优先级提取推理文本，不再将其转成消息；
  - 把提取到的文本作为 `reasoning_content` 附加到**紧随其后的 assistant 消息**上（reasoning 项在 Responses 输出顺序里总是位于同轮 assistant 消息之前；也覆盖 function_call 场景——附加到工具调用创建的 assistant 消息）；
  - 兼容客户端把推理内联在 assistant 项里的写法（item 级 `reasoning_content` / `reasoning` 字符串字段、`summary` 数组）；
  - 连续多个 reasoning 项用 `\n\n` 拼接；同一文本在 `content`/`summary` 重复出现时自动去重；孤儿 reasoning（后面没有 assistant 消息）安全丢弃。
  - 响应侧（Chat→Responses 输出带 `summary` 字段）官方 main 已自带，无需修改，闭环成立。

- **注意事项**：
  - 官方 PR #6998（Claude 协议路径的 thinking signature 回传）与本修复路径不同、互不包含，本仓库**不包含** PR #6998 的改动；
  - 字段名选择有实证依据：CLIProxyAPI issue #4893 确认 OpenCode Zen 明确要求 `reasoning_content`（不是 `reasoning` 也不是 `reasoning_details`）；
  - 设置面板没有任何选项能替代此修复（`thinking_to_content` 只会帮倒忙，`透传请求体` 对 Responses 协议客户端无效）——这是结构转换问题，只能改代码。

---

### 修复 2：Playground 粘贴终端日志导致整页崩溃（Maximum call stack size exceeded）

**涉及文件**：`web/src/components/ui/markdown.tsx`（`renderMarkdown`）、`web/src/components/ai-elements/response.tsx`（`parseMarkdownToStructure` 调用处）

- **问题现象**：在游乐场（Playground）粘贴包含大量连续列表标记的文本（典型：Claude Code 终端日志里的 `+ `/`● ` 行）后，页面立即崩溃并永久显示 "500 糟糕！出错了"，浏览器控制台报 `RangeError: Maximum call stack size exceeded`，且**每次刷新都复现**。

- **根因分析**：
  - 前端用 marked（v18）渲染 Markdown，其列表/内联标记解析是**递归实现**；单行内出现约 1800+ 个连续列表标记（如 `+ `.repeat(1800)）就会超出 JS 调用栈上限（已用同版本库本地复现验证，阈值随调用栈深度浮动）；
  - Playground 的聊天记录持久化在浏览器 `localStorage`（key：`playground_messages`），崩溃的那条消息一直留着，导致每次打开页面都重新解析、重新爆栈——表现为"永久 500"；
  - **与后端、数据库完全无关**（当时的 API 请求实际都是 200 成功），"500"只是前端错误页的默认标题。

- **修复方案**：在两处 Markdown 解析入口包 try/catch，解析爆栈时该条消息**降级为纯文本原样显示**（`<pre>` 包裹），并 `console.error` 记录，保证单条坏消息永远不会再打崩整个页面。原有 40k 字符截断、1MB 存储上限等既有防护保持不变。

- **临时自救命令**（对旧版本，清掉 localStorage 里的毒消息）：

  ```js
  localStorage.removeItem('playground_messages'); location.reload();
  ```

- **使用建议**：往 Playground 粘贴终端日志时包在 ``` 代码块里，代码块内容不走行内 Markdown 解析，最安全。

---

### 修复 3（基础设施）：Windows exe 自动构建与滚动 Release

**涉及文件**：`.github/workflows/build-windows-patched.yml`（新增）

- **官方现状**：官方 Release 只提供 Docker 镜像和 Linux/macOS 二进制，Windows exe 有时提供但不含我们的补丁。
- **本仓库方案**：见下方"接手指南"。

---

## 接手指南

<details>
<summary><b>📖 点击展开：如何接手这份工作（新人必读）</b></summary>

### 当前开发进度

- main 分支 = 官方 main（同步自 `0ed497f0`，2026-09 时点）+ 1 个补丁提交（上述修复 1 + 2 + 3 全部内容）。
- 官方 PR #6998 的 Claude signature 修复**未包含**（路径不同，非必需）。上游如合并了相关修复，可重新评估是否还需要我们的补丁。
- 本地 `D:\new-api\` 是生产环境：`one-api.db`（SQLite 数据库）+ Windows 服务 `new-api-service`（WinSW 包装，配置文件 `new-api-service.xml`，`<executable>` 指向 `D:\new-api\new-api-v1.0.0-rc.30-fix-bugs-patched.exe`）。**改完代码重新部署 = 替换该 exe 文件 + 重启服务**。

### 如何从零构建 patched exe（两种方式）

**方式 A：GitHub Actions 云端构建（推荐，本机零依赖）**

1. `git push` 到本仓库的 `main` 或任意 `fix-*` 分支；
2. 到 [Actions 页面](https://github.com/Karbo123/new-api/actions) 看 "Build Windows exe (patched)" 工作流（bun 构建前端 + Go 构建后端，约 6 分钟）；
3. main 分支构建成功后自动发布/更新滚动 Release `patched-latest`（固定附件名 `new-api-patched-windows.exe`）；`fix-*` 分支只产出 Actions artifact（90 天有效期）。

**方式 B：本地构建**

1. 安装 Bun（前端）和 Go ≥ 1.25（后端）；
2. 前端：`cd web && bun install --frozen-lockfile && bun run build`（产物在 `web/dist`，会被 Go embed）；
3. 后端：`go build -ldflags "-s -w -X 'github.com/QuantumNous/new-api/common.Version=$(git describe --tags)'" -o new-api.exe`。

### 如何跑测试

- 后端转换层：`cd relaykit && go test ./...`（推理回传的用例在 `relayconvert/internal/oai_responses/to_oai_chat_req_reasoning_test.go`）；
- golden 快照测试在 `relaykit/relayconvert/testdata/golden/`，改了响应结构需同步更新（注意 JSON 字段顺序要与 Go struct 定义一致）。

### 如何发布新版本

`git push origin main` 即可，CI 全自动。手动触发：Actions 页面选该工作流 → Run workflow。

### 如何把官方更新同步进来

```bash
git remote add upstream https://github.com/QuantumNous/new-api.git   # 首次
git fetch upstream main
git checkout -B main upstream/main          # 或 merge/rebase
# 重新套用我们的文件（见下"补丁文件清单"），跑测试，单个补丁提交，force push
git push --force origin main
```

保持"官方最新 + 1 个补丁提交"的整洁历史。**补丁文件清单**（同步后需重新套用的全部文件）：

1. `relaykit/relayconvert/internal/oai_responses/to_oai_chat_req.go`
2. `relaykit/relayconvert/internal/oai_responses/to_oai_chat_req_reasoning_test.go`
3. `relaykit/relayconvert/internal/oai_chat/to_oai_responses_resp_reasoning_test.go`
4. `web/src/components/ui/markdown.tsx`（`renderMarkdown` 的 try/catch 降级）
5. `web/src/components/ai-elements/response.tsx`（`parseMarkdownToStructure` 的 try/catch 降级）
6. `.github/workflows/build-windows-patched.yml`
7. `README.md`（本文件）

### 部署到本机生产的注意事项（重要！）

1. **绝对不要用进程名通配符杀进程**（如 `taskkill /IM new-api*`）——生产服务进程名就是 `new-api-*`，误杀会导致网关下线（本机其他服务依赖它）；
2. 测试新 exe 一律用**独立端口 + 独立 SQLite**（如 `PORT=3211` 在临时空目录启动），绝不直连生产库；
3. 服务运行时 exe 文件被锁定，替换方法：`mv 旧.exe 旧.exe.old`（Windows 允许重命名运行中的文件）→ 放入新 exe → 重启服务 → 删除 `.old`；
4. 重启服务后在浏览器 **Ctrl+Shift+R 强刷**前端（跨版本升级 JS chunk 路径可能变化）。

### 其他注意事项

- 本仓库 README 是**完全重写**的（不含官方介绍），同步官方代码时注意保留本文件；
- `relaykit` 是独立 Go module，与主模块通过 replace 引用；
- 历史上本仓库曾短暂包含过 PR #6998 的补丁（后来按需求移除），考古时不要混淆。

</details>
