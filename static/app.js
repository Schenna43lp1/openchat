(function () {
  const messagesEl = document.getElementById("messages");
  const formEl = document.getElementById("messageForm");
  const inputEl = document.getElementById("messageInput");
  const joinFormEl = document.getElementById("joinForm");
  const joinCardEl = document.getElementById("joinCard");
  const usernameInputEl = document.getElementById("usernameInput");
  const usersEl = document.getElementById("users");
  const userCountEl = document.getElementById("userCount");
  const connectionEl = document.getElementById("connectionStatus");
  const connectionTextEl = document.getElementById("connectionText");

  let socket = null;
  let username = localStorage.getItem("go-chat-username") || "";
  let reconnectTimer = null;
  let reconnectDelay = 800;
  let manuallyClosed = false;

  if (username) {
    usernameInputEl.value = username;
  }

  joinFormEl.addEventListener("submit", function (event) {
    event.preventDefault();
    const nextUsername = usernameInputEl.value.trim().replace(/\s+/g, " ");
    if (!nextUsername) {
      return;
    }

    username = nextUsername.slice(0, 32);
    localStorage.setItem("go-chat-username", username);
    joinCardEl.classList.add("is-hidden");
    connect();
  });

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

  function connect() {
    if (!username) {
      return;
    }

    clearTimeout(reconnectTimer);
    setStatus("connecting", "Verbinde...");

    const protocol = window.location.protocol === "https:" ? "wss:" : "ws:";
    const url = `${protocol}//${window.location.host}/ws?username=${encodeURIComponent(username)}`;
    socket = new WebSocket(url);

    socket.addEventListener("open", function () {
      reconnectDelay = 800;
      setStatus("online", "Online");
    });

    socket.addEventListener("message", function (event) {
      const payload = JSON.parse(event.data);
      handlePayload(payload);
    });

    socket.addEventListener("close", function () {
      setStatus("offline", "Offline");
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
    clearTimeout(reconnectTimer);
    reconnectTimer = setTimeout(connect, reconnectDelay);
    reconnectDelay = Math.min(reconnectDelay * 1.7, 8000);
  }

  function sendMessage() {
    const text = inputEl.value.trim();
    if (!text || !socket || socket.readyState !== WebSocket.OPEN) {
      return;
    }

    socket.send(JSON.stringify({ message: text }));
    inputEl.value = "";
    resizeComposer();
  }

  function handlePayload(payload) {
    switch (payload.type) {
      case "history":
        messagesEl.textContent = "";
        (payload.messages || []).forEach(addMessage);
        break;
      case "message":
      case "system":
        addMessage(payload);
        break;
      case "users":
        renderUsers(payload.users || []);
        break;
      default:
        break;
    }
  }

  function addMessage(message) {
    const entry = document.createElement("article");
    entry.className = `message ${message.type === "system" ? "system" : ""}`;

    if (message.type === "system") {
      entry.textContent = `${message.time || ""} ${message.message || ""}`;
    } else {
      const meta = document.createElement("div");
      meta.className = "message-meta";

      const user = document.createElement("span");
      user.className = "message-user";
      user.textContent = message.username || "Unbekannt";

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

    users.forEach(function (name) {
      const item = document.createElement("li");
      item.textContent = name;
      usersEl.appendChild(item);
    });
  }

  function setStatus(state, label) {
    connectionEl.dataset.state = state;
    connectionTextEl.textContent = label;
  }

  function resizeComposer() {
    inputEl.style.height = "auto";
    inputEl.style.height = `${Math.min(inputEl.scrollHeight, 144)}px`;
  }
})();
