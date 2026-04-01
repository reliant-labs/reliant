(function () {
  const APOLLO_APP_ID = '69b209f75a57e5000def369c';
  const STATSIG_CONFIG_GLOBAL = 'RELIANT_DOCS_STATSIG_CONFIG';
  const STATSIG_SDK_URL = 'https://cdn.jsdelivr.net/npm/@statsig/js-client@3/build/statsig-js-client.min.js';
  const VISITOR_STORAGE_KEY = 'reliant.docs.statsig.visitor_id';
  const SESSION_STORAGE_KEY = 'reliant.docs.statsig.session_id';

  initApollo();
  initStatsig();

  function initApollo() {
    try {
      const cacheBuster = Math.random().toString(36).substring(7);
      const script = document.createElement('script');

      script.src = 'https://assets.apollo.io/micro/website-tracker/tracker.iife.js?nocache=' + cacheBuster;
      script.async = true;
      script.defer = true;
      script.onload = function () {
        if (!window.trackingFunctions || typeof window.trackingFunctions.onLoad !== 'function') {
          console.warn('[Reliant Docs][Apollo] Tracking functions unavailable after script load');
          return;
        }

        window.trackingFunctions.onLoad({
          appId: APOLLO_APP_ID,
        });
      };
      script.onerror = function () {
        console.warn('[Reliant Docs][Apollo] Failed to load tracker');
      };

      document.head.appendChild(script);
    } catch (error) {
      console.warn('[Reliant Docs][Apollo] Initialization failed', error);
    }
  }

  function initStatsig() {
    window.reliantStatsig = createDisabledStatsigState();

    const config = getStatsigConfig();
    if (!config) {
      return;
    }

    const pendingEvents = [];
    let client = null;
    let ready = false;
    let hasTrackedSearchOpen = false;
    let lastPageViewKey = '';

    const localStorageRef = getStorage('localStorage');
    const sessionStorageRef = getStorage('sessionStorage');
    const visitorID = getOrCreateID(localStorageRef, VISITOR_STORAGE_KEY, 'docs_visitor_');
    const sessionID = getOrCreateID(sessionStorageRef, SESSION_STORAGE_KEY, 'docs_session_');

    const statsigState = {
      client: null,
      ready: false,
      track: track,
      checkGate: function (gateName) {
        if (!client || !ready || typeof client.checkGate !== 'function') {
          return false;
        }
        return client.checkGate(gateName);
      },
      getDynamicConfig: function (configName) {
        if (!client || !ready || typeof client.getDynamicConfig !== 'function') {
          return null;
        }
        return client.getDynamicConfig(configName);
      },
      getClient: function () {
        return client;
      },
      getVisitorID: function () {
        return visitorID;
      },
      getSessionID: function () {
        return sessionID;
      },
      isReady: function () {
        return ready;
      },
    };

    window.reliantStatsig = statsigState;

    function currentPageMetadata(extra) {
      const path = window.location.pathname || '/';
      let referrerHost = '';
      let pageURL = '';

      try {
        referrerHost = document.referrer ? new URL(document.referrer).host : '';
      } catch (_error) {
        referrerHost = '';
      }

      try {
        pageURL = window.location.href || '';
      } catch (_error) {
        pageURL = '';
      }

      return Object.assign(
        {
          path: path,
          page_url: cleanString(pageURL, 300),
          page_title: cleanString(document.title, 200),
          referrer_host: referrerHost,
          section: sectionFromPath(path),
          is_homepage: path === '/',
          session_id: sessionID,
        },
        extra || {},
      );
    }

    function sendEvent(name, metadata) {
      if (!client || !ready) {
        pendingEvents.push([name, metadata || {}]);
        return;
      }

      try {
        client.logEvent(name, undefined, metadata || {});
      } catch (error) {
        console.warn('[Reliant Docs][Statsig] Failed to log event', name, error);
      }
    }

    function track(name, metadata) {
      sendEvent(name, currentPageMetadata(metadata));
    }

    function flushPendingEvents() {
      while (pendingEvents.length > 0) {
        const nextEvent = pendingEvents.shift();
        if (!nextEvent) {
          continue;
        }

        sendEvent(nextEvent[0], nextEvent[1]);
      }
    }

    function trackPageView() {
      const pageViewKey = [window.location.pathname || '/', window.location.search || '', cleanString(document.title, 200)].join('|');
      if (pageViewKey === lastPageViewKey) {
        return;
      }

      lastPageViewKey = pageViewKey;
      track('docs_page_view');
    }

    function initializeClient() {
      if (!window.Statsig || !window.Statsig.StatsigClient) {
        console.warn('[Reliant Docs][Statsig] SDK loaded without StatsigClient');
        return;
      }

      client = new window.Statsig.StatsigClient(
        config.clientKey,
        {
          userID: visitorID,
        },
        {
          environment: {
            tier: config.environment,
          },
        },
      );

      statsigState.client = client;

      client
        .initializeAsync()
        .then(function () {
          ready = true;
          statsigState.ready = true;
          trackPageView();
          flushPendingEvents();
        })
        .catch(function (error) {
          console.warn('[Reliant Docs][Statsig] Initialization failed', error);
        });
    }

    function loadSDK() {
      if (window.Statsig && window.Statsig.StatsigClient) {
        initializeClient();
        return;
      }

      const script = document.createElement('script');
      script.src = config.sdkURL;
      script.async = true;
      script.onload = initializeClient;
      script.onerror = function () {
        console.warn('[Reliant Docs][Statsig] Failed to load browser SDK');
      };
      document.head.appendChild(script);
    }

    function schedulePageView() {
      window.setTimeout(trackPageView, 0);
    }

    document.addEventListener('click', function (event) {
      const eventTarget = event.target;
      if (!(eventTarget instanceof Element)) {
        return;
      }

      const explicitTarget = eventTarget.closest('[data-statsig-event]');
      if (explicitTarget) {
        const eventName = explicitTarget.getAttribute('data-statsig-event');
        if (eventName) {
          const explicitMetadata = {
            element_text: cleanString(explicitTarget.textContent, 120),
            href: cleanString(explicitTarget.getAttribute('href'), 300),
          };

          const label = explicitTarget.getAttribute('data-statsig-label');
          const surface = explicitTarget.getAttribute('data-statsig-surface');
          const ctaId = explicitTarget.getAttribute('data-statsig-id');
          if (label) {
            explicitMetadata.label = cleanString(label, 120);
          }
          if (surface) {
            explicitMetadata.surface = cleanString(surface, 80);
          }
          if (ctaId) {
            explicitMetadata.cta_id = cleanString(ctaId, 120);
          }

          track(eventName, explicitMetadata);
        }
        return;
      }

      const link = eventTarget.closest('a[href]');
      if (!link) {
        return;
      }

      const href = link.getAttribute('href') || '';
      if (!href || href.startsWith('#') || href.startsWith('mailto:') || href.startsWith('tel:')) {
        return;
      }

      let destination;
      try {
        destination = new URL(link.href, window.location.origin);
      } catch (_error) {
        return;
      }

      if (destination.origin === window.location.origin) {
        schedulePageView();
        return;
      }

      track('docs_outbound_click', {
        link_url: cleanString(destination.toString(), 300),
        link_host: destination.host,
        link_text: cleanString(link.textContent, 120),
      });
    });

    document.addEventListener('focusin', function (event) {
      if (hasTrackedSearchOpen) {
        return;
      }

      const target = event.target;
      if (!(target instanceof HTMLInputElement)) {
        return;
      }

      if (!isSearchInput(target)) {
        return;
      }

      hasTrackedSearchOpen = true;
      track('docs_search_open');
    });

    document.addEventListener('keydown', function (event) {
      if (event.key !== 'Enter') {
        return;
      }

      const target = event.target;
      if (!(target instanceof HTMLInputElement)) {
        return;
      }

      if (!isSearchInput(target)) {
        return;
      }

      const query = cleanString(target.value, 200);
      if (!query) {
        return;
      }

      track('docs_search_submit', {
        query_length: query.length,
      });
    });

    instrumentHistory(schedulePageView);
    window.addEventListener('popstate', schedulePageView);
    window.addEventListener('hashchange', schedulePageView);

    if (document.readyState === 'loading') {
      document.addEventListener('DOMContentLoaded', loadSDK, { once: true });
      return;
    }

    loadSDK();
  }

  function createDisabledStatsigState() {
    return {
      client: null,
      ready: false,
      track: function () {},
      checkGate: function () {
        return false;
      },
      getDynamicConfig: function () {
        return null;
      },
      getClient: function () {
        return null;
      },
      getVisitorID: function () {
        return null;
      },
      getSessionID: function () {
        return null;
      },
      isReady: function () {
        return false;
      },
    };
  }

  function getStatsigConfig() {
    const runtimeConfig = window[STATSIG_CONFIG_GLOBAL];
    if (!runtimeConfig || runtimeConfig.enabled !== true) {
      return null;
    }

    if (!runtimeConfig.clientKey) {
      console.warn('[Reliant Docs][Statsig] Config present but missing clientKey');
      return null;
    }

    return {
      clientKey: String(runtimeConfig.clientKey),
      environment: String(runtimeConfig.environment || 'production'),
      sdkURL: String(runtimeConfig.sdkURL || STATSIG_SDK_URL),
    };
  }

  function getStorage(kind) {
    try {
      return window[kind];
    } catch (_error) {
      return null;
    }
  }

  function generateID(prefix) {
    if (window.crypto && typeof window.crypto.randomUUID === 'function') {
      return prefix + window.crypto.randomUUID();
    }

    return prefix + Math.random().toString(36).slice(2) + Date.now().toString(36);
  }

  function getOrCreateID(storage, key, prefix) {
    if (!storage) {
      return generateID(prefix);
    }

    let value = storage.getItem(key);
    if (!value) {
      value = generateID(prefix);
      storage.setItem(key, value);
    }

    return value;
  }

  function cleanString(value, maxLength) {
    return String(value || '')
      .trim()
      .replace(/\s+/g, ' ')
      .slice(0, maxLength || 160);
  }

  function sectionFromPath(path) {
    const segments = String(path || '/')
      .replace(/^\/+|\/+$/g, '')
      .split('/')
      .filter(Boolean);

    if (segments.length === 0) {
      return 'home';
    }

    return segments[0];
  }

  function isSearchInput(target) {
    if (!(target instanceof HTMLInputElement)) {
      return false;
    }

    const identifier = (target.id || '').toLowerCase();
    const name = (target.name || '').toLowerCase();
    const placeholder = (target.getAttribute('placeholder') || '').toLowerCase();

    return identifier === 'search-input' || name === 'search' || placeholder.indexOf('search') !== -1;
  }

  function instrumentHistory(onChange) {
    if (window.__reliantDocsHistoryInstrumented) {
      return;
    }

    window.__reliantDocsHistoryInstrumented = true;

    const originalPushState = window.history.pushState;
    const originalReplaceState = window.history.replaceState;

    window.history.pushState = function () {
      const result = originalPushState.apply(window.history, arguments);
      onChange();
      return result;
    };

    window.history.replaceState = function () {
      const result = originalReplaceState.apply(window.history, arguments);
      onChange();
      return result;
    };
  }
})();
