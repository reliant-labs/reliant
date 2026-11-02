import { describe, expect, it } from 'vitest';

import {
  devServerUrl,
  isDevUrl,
  isLoopbackHostname,
  isPreviewUrl,
  isSameDevPage,
  toPreviewUrl,
} from '../dev-preview';

const CLOUD = 'https://{port}-d1.workspaces.example.com';
const PATH_DEV = 'http://localhost:8080/proxy/d1/{port}/';

describe('isLoopbackHostname', () => {
  it.each(['localhost', 'LOCALHOST', 'app.localhost', '127.0.0.1', '127.1.2.3', '0.0.0.0', '[::1]'])(
    '%s is loopback',
    (host) => expect(isLoopbackHostname(host)).toBe(true),
  );
  it.each(['example.com', 'localhost.example.com', '10.0.0.1', '128.0.0.1'])('%s is not', (host) =>
    expect(isLoopbackHostname(host)).toBe(false),
  );
});

describe('isPreviewUrl', () => {
  it("matches the template's daemon on any port", () => {
    expect(isPreviewUrl('https://5173-d1.workspaces.example.com/', CLOUD)).toBe(true);
    expect(isPreviewUrl('https://5173-d1.workspaces.example.com', CLOUD)).toBe(true);
    expect(isPreviewUrl('https://5173-D1.Workspaces.example.com/a?b#c', CLOUD)).toBe(true);
  });

  it('rejects another daemon, a look-alike host and no template', () => {
    expect(isPreviewUrl('https://5173-d2.workspaces.example.com/', CLOUD)).toBe(false);
    expect(isPreviewUrl('https://5173-d1.workspaces.example.com.evil.test/', CLOUD)).toBe(false);
    expect(isPreviewUrl('https://5173-d1.workspaces.example.com/', undefined)).toBe(false);
  });

  it('matches a path-mode dev template', () => {
    expect(isPreviewUrl('http://localhost:8080/proxy/d1/3000/', PATH_DEV)).toBe(true);
    expect(isPreviewUrl('http://localhost:8080/other', PATH_DEV)).toBe(false);
  });
});

describe('isDevUrl', () => {
  it('is loopback or a preview URL, and nothing else', () => {
    expect(isDevUrl('http://localhost:3000', undefined)).toBe(true);
    expect(isDevUrl('http://127.0.0.1:3000/x', undefined)).toBe(true);
    expect(isDevUrl('https://5173-d1.workspaces.example.com/', CLOUD)).toBe(true);
    expect(isDevUrl('https://github.com/', CLOUD)).toBe(false);
    expect(isDevUrl('mailto:a@b.c', CLOUD)).toBe(false);
    expect(isDevUrl('not a url', CLOUD)).toBe(false);
  });
});

describe('toPreviewUrl', () => {
  it('maps a loopback URL onto the daemon, keeping path, query and fragment', () => {
    expect(toPreviewUrl('http://localhost:5173/a/b?c=1#d', CLOUD)).toBe(
      'https://5173-d1.workspaces.example.com/a/b?c=1#d',
    );
    expect(toPreviewUrl('http://127.0.0.1:3000', CLOUD)).toBe('https://3000-d1.workspaces.example.com/');
    expect(toPreviewUrl('http://localhost:3000/x', PATH_DEV)).toBe('http://localhost:8080/proxy/d1/3000/x');
  });

  it('leaves alone what is not a remote loopback URL', () => {
    // A local daemon: localhost is the daemon's.
    expect(toPreviewUrl('http://localhost:5173/', undefined)).toBeNull();
    // Already a preview URL — in path-mode dev that is a localhost URL too.
    expect(toPreviewUrl('http://localhost:8080/proxy/d1/3000/', PATH_DEV)).toBeNull();
    expect(toPreviewUrl('https://github.com/', CLOUD)).toBeNull();
    expect(toPreviewUrl('http://localhost/', CLOUD)).toBeNull();
  });
});

describe('devServerUrl', () => {
  it('is the preview URL with a template, localhost without', () => {
    expect(devServerUrl(5173, CLOUD)).toBe('https://5173-d1.workspaces.example.com');
    expect(devServerUrl(5173, undefined)).toBe('http://localhost:5173');
  });
});

describe('isSameDevPage', () => {
  it('matches any page of the server', () => {
    expect(isSameDevPage('http://localhost:5173/', 'http://localhost:5173')).toBe(true);
    expect(isSameDevPage('http://localhost:5173/settings?x=1', 'http://localhost:5173')).toBe(true);
    expect(isSameDevPage('https://5173-d1.workspaces.example.com/__proxy/callback?token=t', devServerUrl(5173, CLOUD))).toBe(
      true,
    );
    expect(isSameDevPage('http://localhost:8080/proxy/d1/3000/app', devServerUrl(3000, PATH_DEV))).toBe(true);
  });

  it('does not match another port or server', () => {
    expect(isSameDevPage('http://localhost:51730/', 'http://localhost:5173')).toBe(false);
    expect(isSameDevPage('http://localhost:3000/', 'http://localhost:5173')).toBe(false);
    expect(isSameDevPage('http://localhost:8080/proxy/d1/30001/', devServerUrl(3000, PATH_DEV))).toBe(false);
  });
});
