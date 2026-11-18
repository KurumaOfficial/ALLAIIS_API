# Browser bridge for an opt-in web application

The reference bridge connects an OpenAI-compatible client to a web application
that explicitly implements a worker. The example serves its own worker page
and a deterministic text echo model. Use this example to test browser
connectivity and cancellation before defining an adapter for an authorized
application.

## Connection

1. ALLAIIS calls a local adapter through a registered OpenAI-compatible route.
   The adapter admits one request at a time and assigns it a unique ID.
2. The app's worker page claims the request through a same-origin endpoint. The
   worker uses its own key, separate from the API credential.
3. The app replies with the same ID. The adapter returns JSON or wraps the
   finished text in compatible SSE events. Client disconnects and timeouts
   release the request. Late or duplicate replies are rejected.

The page and the adapter run on the same origin. The worker holds its key in
memory and the listener binds to IPv4 loopback. Browser cookies and browser
profiles are outside this contract. The example's echo does not make inference
requests to external services.

## Run locally

```sh
go run ./examples/browser-bridge
```

Open the exact browser URL shown in the terminal, enter the worker code, and
click **Connect worker**. The terminal also shows the upstream base URL and
the separate upstream API key.

Add this provider to your ALLAIIS `config.json`. Adjust the port if you
started the example with `--port`. For a fresh installation, build and
initialize ALLAIIS using the [quick start](../README.md#quick-start).

```json
{
  "providers": {
    "browser-demo": {
      "base_url": "http://127.0.0.1:8430/v1"
    }
  },
  "models": []
}
```

```sh
./allaiis account add --provider browser-demo --label local
./allaiis models add --provider browser-demo --upstream-model browser-echo --label local
./allaiis up
```

Enter the example's upstream API key at the first prompt for a provider key.
The client can select `browser-echo-browser-demo` using the ALLAIIS endpoint
and local gateway key. The existing `allaiis export opencode` command prints
the provider configuration for OpenCode. Keep the worker tab open while
making requests.

## Run on a server

Run the example and ALLAIIS on the server. Keep both listeners on loopback
and forward the ports to your workstation:

```sh
ssh -L 8430:127.0.0.1:8430 -L 8420:127.0.0.1:8420 user@server
```

Open `http://127.0.0.1:8430/` in your workstation browser. The worker runs
in that tab while both processes stay on the server. For automated integration
tests, an isolated browser can open the same worker page using a disposable
test profile.

The current example supports single-turn text and a single in-flight request.
A production adapter would also need an explicit permission grant from the app
owner, a versioned capability manifest, independently scoped workers, and
native incremental events for apps that actually stream replies.
