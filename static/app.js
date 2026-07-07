(function () {
  // Main chat client: handles socket lifecycle, rendering and composer UX.
  const messagesEl = document.getElementById("messages");
  const formEl = document.getElementById("messageForm");
  const inputEl = document.getElementById("messageInput");
  const directTargetEl = document.getElementById("directTarget");
  const usersEl = document.getElementById("users");
  const userCountEl = document.getElementById("userCount");
  const connectionEl = document.getElementById("connectionStatus");
  const connectionTextEl = document.getElementById("connectionText");
  const splashEl = document.getElementById("splashScreen");
  const currentUserEl = document.getElementById("currentUser");
  const currentUsername = currentUserEl ? currentUserEl.textContent.trim() : "";
  const chatScope = document.body.dataset.chatScope || "public";

  let socket = null;
  let reconnectTimer = null;
  let reconnectDelay = 800;
  let manuallyClosed = false;
  let splashHidden = false;
  const splashStartedAt = Date.now();

  formEl.addEventListener("submit", function (event) {
    event.preventDefault();
    sendMessage();
  });

  inputEl.addEventListener("keydown", function (event) {
    if (event.key === "Enter" && !event.shiftKey) {
      event.preventDefault();
      sendMessage();
    }
  });

  inputEl.addEventListener("input", resizeComposer);

  window.addEventListener("beforeunload", function () {
    manuallyClosed = true;
    if (socket) {
      socket.close();
    }
  });

  setTimeout(hideSplash, 1800);
  connect();

  function connect() {
    clearTimeout(reconnectTimer);
    setStatus("connecting", "Verbinde...");

    const protocol = window.location.protocol === "https:" ? "wss:" : "ws:";
    const url = `${protocol}//${window.location.host}/ws`;
    socket = new WebSocket(url);

    socket.addEventListener("open", function () {
      reconnectDelay = 800;
      setStatus("online", "Online");
      hideSplash();
    });

    socket.addEventListener("message", function (event) {
      const payload = JSON.parse(event.data);
      handlePayload(payload);
    });

    socket.addEventListener("close", function (event) {
      setStatus("offline", "Offline");
      // Policy/protocol close codes indicate auth/session problems.
      if (event.code === 1008 || event.code === 1002) {
        window.location.href = "/login";
        return;
      }
      if (!manuallyClosed) {
        scheduleReconnect();
      }
    });

    socket.addEventListener("error", function () {
      setStatus("offline", "Fehler");
      socket.close();
    });
  }

  function scheduleReconnect() {
    // Exponential backoff avoids aggressive reconnect storms.
    clearTimeout(reconnectTimer);
    reconnectTimer = setTimeout(connect, reconnectDelay);
    reconnectDelay = Math.min(reconnectDelay * 1.7, 8000);
  }

  function sendMessage() {
    const text = inputEl.value.trim();
    if (!text || !socket || socket.readyState !== WebSocket.OPEN) {
      return;
    }

    const recipient = directTargetEl ? directTargetEl.value.trim() : "";
    if (directTargetEl && recipient === "") {
      directTargetEl.focus();
      return;
    }
    const payload = { message: text };
    if (recipient !== "") {
      payload.to = recipient;
    }

    socket.send(JSON.stringify(payload));
    inputEl.value = "";
    resizeComposer();
  }

  function handlePayload(payload) {
    switch (payload.type) {
      case "history":
        messagesEl.textContent = "";
        (payload.messages || []).filter(shouldRenderMessage).forEach(addMessage);
        break;
      case "message":
      case "direct":
      case "system":
        if (shouldRenderMessage(payload)) {
          addMessage(payload);
        }
        break;
      case "users":
        renderUsers(payload.users || []);
        break;
      default:
        break;
    }
  }

  function shouldRenderMessage(message) {
    if (!message) {
      return false;
    }
    if (chatScope === "direct") {
      return message.type === "direct" || message.type === "system";
    }
    return message.type === "message" || message.type === "system";
  }

  function addMessage(message) {
    const entry = document.createElement("article");
    entry.className = `message ${message.type === "system" ? "system" : ""} ${message.type === "direct" ? "direct" : ""}`;

    if (message.type === "system") {
      entry.textContent = `${message.time || ""} ${message.message || ""}`;
    } else {
      const meta = document.createElement("div");
      meta.className = "message-meta";

      const user = document.createElement("span");
      user.className = "message-user";
      if (message.type === "direct") {
        const direction = currentUsername && message.username === currentUsername ? `an ${message.to || "?"}` : `von ${message.username || "Unbekannt"}`;
        user.textContent = `Direkt ${direction}`;
      } else {
        user.textContent = message.username || "Unbekannt";
      }

      const time = document.createElement("span");
      time.className = "message-time";
      time.textContent = message.time || "";

      const text = document.createElement("p");
      text.className = "message-text";
      text.textContent = message.message || "";

      meta.append(user, time);
      entry.append(meta, text);
    }

    messagesEl.appendChild(entry);
    messagesEl.scrollTop = messagesEl.scrollHeight;
  }

  function renderUsers(users) {
    usersEl.textContent = "";
    userCountEl.textContent = String(users.length);
    const currentTarget = directTargetEl ? directTargetEl.value : "";

    users.forEach(function (name) {
      const item = document.createElement("li");
      item.textContent = name;
      usersEl.appendChild(item);
    });

    if (!directTargetEl) {
      return;
    }
    directTargetEl.textContent = "";
    const allOption = document.createElement("option");
    allOption.value = "";
    allOption.textContent = chatScope === "direct" ? "Empfaenger auswaehlen" : "Alle (oeffentlicher Chat)";
    directTargetEl.appendChild(allOption);

    users
      .filter(function (name) { return name !== currentUsername; })
      .forEach(function (name) {
        const option = document.createElement("option");
        option.value = name;
        option.textContent = `Direkt an ${name}`;
        directTargetEl.appendChild(option);
      });

    const stillAvailable = Array.from(directTargetEl.options).some(function (option) {
      return option.value === currentTarget;
    });
    directTargetEl.value = stillAvailable ? currentTarget : "";
  }

  function setStatus(state, label) {
    connectionEl.dataset.state = state;
    connectionTextEl.textContent = label;
  }

  function hideSplash() {
    if (!splashEl || splashHidden) {
      return;
    }
    const elapsed = Date.now() - splashStartedAt;
    if (elapsed < 550) {
      // Keep splash visible for a short minimum duration to avoid flicker.
      setTimeout(hideSplash, 550 - elapsed);
      return;
    }
    splashHidden = true;
    splashEl.classList.add("is-hidden");
    setTimeout(function () {
      splashEl.remove();
    }, 300);
  }

  function resizeComposer() {
    inputEl.style.height = "auto";
    inputEl.style.height = `${Math.min(inputEl.scrollHeight, 144)}px`;
  }
})();
