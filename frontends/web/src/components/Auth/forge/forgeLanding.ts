// Where a forge sign-in screen goes once the person is signed in.
//
// A full navigation, not a router navigate, the same call forge's scaffold
// makes: every provider and cache then starts again with the session already
// in place (authStore.initialize reads /auth/session). Its own module so a
// test can replace it without redefining window.location.
export function landAt(path: string): void {
  window.location.assign(path)
}
