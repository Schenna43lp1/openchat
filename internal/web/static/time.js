// Shows times in the zone and on the calendar of the browser. The server does not know either.
// Elements: <time datetime="RFC 3339" data-local="auto|date">. "auto" leaves out the date of today.
(function () {
  const today = new Intl.DateTimeFormat("de-DE", { hour: "2-digit", minute: "2-digit", second: "2-digit" });
  const dated = new Intl.DateTimeFormat("de-DE", { day: "2-digit", month: "2-digit", year: "numeric", hour: "2-digit", minute: "2-digit" });

  function show(element) {
    const date = new Date(element.dateTime);
    if (Number.isNaN(date.getTime())) {
      return; // keep what the server wrote
    }
    const withDate = element.dataset.local === "date" || date.toDateString() !== new Date().toDateString();
    const text = (withDate ? dated : today).format(date);
    // Only on a change, the message list is a live region.
    if (element.textContent !== text) {
      element.textContent = text;
    }
  }

  function showAll() {
    document.querySelectorAll("time[data-local]").forEach(show);
  }

  // create takes Unix milliseconds. Imported messages can be without a time, then it stays empty.
  function create(timestamp, className) {
    const element = document.createElement("time");
    element.className = className || "";
    const date = new Date(timestamp || NaN);
    if (!Number.isNaN(date.getTime())) {
      element.dateTime = date.toISOString();
      element.dataset.local = "auto";
      show(element);
    }
    return element;
  }

  window.openchatTime = { create: create };
  showAll();
  // At midnight "today" becomes a date.
  setInterval(showAll, 60000);
})();
