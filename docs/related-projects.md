# Related projects

These independent projects explore API compatibility for web chat products.
ALLAIIS uses provider-issued API keys and includes a separate browser example
for [applications that implement its worker protocol](browser-bridge.md).

| Project | Relevant design |
| --- | --- |
| [crisazas/opencode-venice-web](https://github.com/crisazas/opencode-venice-web) | Local model listing and Chat Completions for OpenCode. The project documents stream behavior and the limits of emulated tool calls. |
| [RfadnjdExt/venice-ai-chat-proxy](https://github.com/RfadnjdExt/venice-ai-chat-proxy) | OpenAI-style endpoints for Venice with streaming, account pools, and request-size error handling. |
| [despin/venice-ai-chat-proxy](https://github.com/despin/venice-ai-chat-proxy) | An OpenAI-compatible proxy for Venice with local and Docker deployment. |
| [lanqian528/chat2api](https://github.com/lanqian528/chat2api) | An example of exposing a ChatGPT web interface through an OpenAI-style API. |
| [aintlikeu/unofficial-venice-proxy](https://github.com/aintlikeu/unofficial-venice-proxy) | Upstream of RfadnjdExt's Venice proxy. |
| [CanArslanDev/opencode-venice-web](https://github.com/CanArslanDev/opencode-venice-web) | Upstream of crisazas's OpenCode integration. |
| [weizsw/magpie-venice-web](https://github.com/weizsw/magpie-venice-web) | A Venice integration packaged as a client plugin. |
| [Parsa-Nasiri/venice-free-ai-chat](https://github.com/Parsa-Nasiri/venice-free-ai-chat) | A Venice chat UI project. |
| [rosterbrink-awp/ChatGPT-Web2API](https://github.com/rosterbrink-awp/ChatGPT-Web2API) | A browser-backed service exposing REST and MCP interfaces. |
| [stufently/gpt-web-gateway](https://github.com/stufently/gpt-web-gateway) | A web gateway covering chat and image generation. |
| [zqbxdev/webchat2api](https://github.com/zqbxdev/webchat2api) | A web chat gateway spanning multiple providers. |

RfadnjdExt's treatment of oversized conversations prompted ALLAIIS to handle
upstream HTTP `413` explicitly. ALLAIIS retains that status and returns a
protocol-specific size error so clients can choose how to reduce the request.
The gateway implementation and tests are original Go code.

The local compatibility fixtures use the documented event names and tool
encodings as references. The SSE files contain synthetic data.
[Inference response events](https://github.com/crisazas/opencode-venice-web/blob/main/docs/PROTOCOL.md#3-inference-response) ·
[Tool envelope formats](https://github.com/RfadnjdExt/venice-ai-chat-proxy#tool-calling-emulated)
