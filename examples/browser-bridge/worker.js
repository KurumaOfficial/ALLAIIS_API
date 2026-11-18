// SPDX-License-Identifier: GPL-3.0-only

const form = document.querySelector("#connect");
const field = document.querySelector("#code");
const disconnect = document.querySelector("#disconnect");
const statusLine = document.querySelector("#status");
let active = null;

function stop(message) {
  active?.abort();
  active = null;
  form.hidden = false;
  disconnect.hidden = true;
  statusLine.textContent = message;
}

disconnect.addEventListener("click", () => stop("Worker disconnected."));

form.addEventListener("submit", (event) => {
  event.preventDefault();
  const key = field.value.trim();
  field.value = "";
  const worker = new AbortController();
  active = worker;
  form.hidden = true;
  disconnect.hidden = false;
  poll(worker, key);
});

async function poll(worker, key) {
  async function request(path, body) {
    const headers = { Authorization: "Bearer " + key };
    if (body) headers["Content-Type"] = "application/json";
    return fetch(path, {
      method: "POST",
      headers,
      body: body && JSON.stringify(body),
      signal: worker.signal,
    });
  }
  try {
    while (!worker.signal.aborted) {
      const claim = await request("/worker/claim");
      if (!claim.ok) throw new Error("Could not connect. Check your worker code and bridge URL.");
      if (claim.status === 200) {
        const { id, text } = await claim.json();
        const reply = await request("/worker/reply", { id, text: "Browser echo: " + text });
        if (!reply.ok && reply.status !== 409) throw new Error("The bridge rejected the worker reply.");
      }
      statusLine.textContent = "Worker connected.";
      await new Promise((resolve) => setTimeout(resolve, 300));
    }
  } catch (error) {
    if (active === worker) stop(error.message);
  }
}
