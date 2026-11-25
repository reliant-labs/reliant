// A single-page app with client-side routing. Hosted static hosting serves
// index.html for any unknown extension-less path, so /about works on reload.
const routes = {
  "/": "<h1>Hello from a Vite SPA</h1><p><a href='/about'>About</a></p>",
  "/about":
    "<h1>About</h1><p>Built once, deployed by Reliant Forge.</p><p><a href='/'>Home</a></p>",
};

function render() {
  const html = routes[location.pathname] ?? "<h1>Not found</h1>";
  document.getElementById("app").innerHTML = html;
}

document.addEventListener("click", (event) => {
  const link = event.target.closest("a");
  if (!link || link.origin !== location.origin) return;
  event.preventDefault();
  history.pushState(null, "", link.pathname);
  render();
});

window.addEventListener("popstate", render);
render();
