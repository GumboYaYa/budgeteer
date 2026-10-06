// Import page: show only the fields that belong to the chosen source.
(function () {
  "use strict";
  const radios = document.querySelectorAll("input[name=source]");
  const fields = document.querySelectorAll("[data-source]");
  if (radios.length === 0) return;
  function update() {
    const source = document.querySelector("input[name=source]:checked").value;
    fields.forEach((el) => {
      const active = el.dataset.source === source;
      el.hidden = !active;
      el.querySelectorAll("input, select").forEach((input) => (input.disabled = !active));
    });
  }
  radios.forEach((r) => r.addEventListener("change", update));
  update();
})();
