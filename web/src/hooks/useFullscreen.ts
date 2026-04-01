import { useState, useEffect } from 'react';

export function useFullscreen() {
  const [isFullscreen, setIsFullscreen] = useState(false);
  const [isLoading, setIsLoading] = useState(true);

  useEffect(() => {
    // Check if we're in Electron environment
    if (!window.electronAPI) {
      // In web-only mode, we're never in fullscreen
      setIsFullscreen(false);
      setIsLoading(false);
      return;
    }

    let cancelled = false;

    // Get initial fullscreen status
    const checkInitialStatus = async () => {
      try {
        const status = await window.electronAPI.getFullscreenStatus();
        if (!cancelled) {
          setIsFullscreen(status);
          setIsLoading(false);
        }
      } catch (error) {
        console.error('Failed to get fullscreen status:', error);
        if (!cancelled) {
          setIsLoading(false);
        }
      }
    };

    checkInitialStatus();

    // Listen for fullscreen changes
    const unsubscribe = window.electronAPI.onFullscreenChanged?.((fullscreen: boolean) => {
      setIsFullscreen(fullscreen);
    });

    // Cleanup listener on unmount
    return () => {
      cancelled = true;
      if (typeof unsubscribe === 'function') {
        unsubscribe();
      }
    };
  }, []);

  const toggleFullscreen = async () => {
    if (!window.electronAPI) return;
    
    try {
      const result = await window.electronAPI.toggleFullscreen();
      if (result?.success && result.isFullScreen !== undefined) {
        setIsFullscreen(result.isFullScreen);
      }
    } catch (error) {
      console.error('Failed to toggle fullscreen:', error);
    }
  };

  return { isFullscreen, toggleFullscreen, isLoading };
}