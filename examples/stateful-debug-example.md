# Stateful Component Debugging Example

## The Bug Scenario

A chat application has a bug where messages disappear when:
1. User signs up
2. User sends messages 
3. User logs out
4. User logs back in
5. User navigates to settings page
6. User returns to chat view
7. **BUG**: Messages show as empty (but only on second login + settings visit)

## The State Flow

```
User -> Frontend (React) -> API Gateway -> Auth Service -> Message Service -> PostgreSQL
                                        -> Cache Service -> Redis
                                        -> Session Service -> MongoDB
```

## State Reproduction Harness Implementation

```typescript
// state-reproduction-harness.ts

interface StateSnapshot {
  timestamp: number;
  step: number;
  action: string;
  services: {
    frontend: { route: string; component: string; props: any; state: any };
    authService: { user: any; session: any; tokens: any };
    messageService: { messages: any[]; metadata: any };
    cacheService: { keys: string[]; values: Record<string, any> };
    database: { users: any[]; messages: any[]; sessions: any[] };
  };
  networkCalls: {
    request: { url: string; method: string; headers: any; body: any };
    response: { status: number; headers: any; body: any };
  }[];
}

class StateReproductionHarness {
  private stateHistory: StateSnapshot[] = [];
  private mockResponses: Map<string, any> = new Map();
  private stateVerifiers: Map<string, (state: any) => boolean> = new Map();
  
  async executeStateSequence(): Promise<void> {
    console.log("🔍 Starting State Reproduction Sequence");
    
    // Step 1: User signup
    await this.step(1, "USER_SIGNUP", async () => {
      const response = await this.mockableRequest('/api/auth/signup', {
        method: 'POST',
        body: { email: 'test@example.com', password: 'test123' }
      });
      
      this.captureState({
        authService: { 
          user: { id: 123, email: 'test@example.com', status: 'pending' },
          session: null 
        },
        cacheService: { keys: [], values: {} },
        database: { 
          users: [{ id: 123, email: 'test@example.com', created_at: Date.now() }]
        }
      });
      
      this.verify("User created with pending status", (state) => 
        state.authService.user.status === 'pending'
      );
    });
    
    // Step 2: Send messages
    await this.step(2, "SEND_MESSAGES", async () => {
      const messages = [
        { text: "Hello", timestamp: Date.now() },
        { text: "World", timestamp: Date.now() + 1000 }
      ];
      
      for (const msg of messages) {
        await this.mockableRequest('/api/messages/send', {
          method: 'POST',
          body: msg,
          headers: { 'Authorization': 'Bearer temp-token-123' }
        });
      }
      
      this.captureState({
        messageService: { 
          messages: messages.map((m, i) => ({ ...m, id: i + 1, userId: 123 }))
        },
        cacheService: { 
          keys: ['messages:123'],
          values: { 'messages:123': messages }
        }
      });
      
      this.verify("Messages stored correctly", (state) => 
        state.messageService.messages.length === 2
      );
    });
    
    // Step 3: Logout
    await this.step(3, "USER_LOGOUT", async () => {
      await this.mockableRequest('/api/auth/logout', { method: 'POST' });
      
      this.captureState({
        authService: { 
          user: { id: 123, status: 'pending' },
          session: null  // Session cleared
        },
        cacheService: { 
          keys: [],  // Cache cleared on logout
          values: {}
        },
        frontend: { 
          route: '/login',
          component: 'LoginPage',
          state: { user: null, isAuthenticated: false }
        }
      });
      
      this.verify("Session cleared", (state) => 
        state.authService.session === null
      );
    });
    
    // Step 4: Login again (CRITICAL STATE TRANSITION)
    await this.step(4, "USER_LOGIN_SECOND", async () => {
      const response = await this.mockableRequest('/api/auth/login', {
        method: 'POST',
        body: { email: 'test@example.com', password: 'test123' }
      });
      
      this.captureState({
        authService: { 
          user: { id: 123, status: 'active' },  // Status changes to active
          session: { token: 'new-token-456', userId: 123 }
        },
        cacheService: { 
          keys: ['session:456', 'user:123'],
          values: { 
            'session:456': { userId: 123, created: Date.now() },
            'user:123': { id: 123, status: 'active' }
            // NOTE: messages NOT in cache yet
          }
        }
      });
      
      this.verify("User status changed to active", (state) => 
        state.authService.user.status === 'active'
      );
    });
    
    // Step 5: Navigate to settings (TRIGGERS CACHE REFRESH)
    await this.step(5, "NAVIGATE_SETTINGS", async () => {
      await this.mockableRequest('/api/user/settings', { method: 'GET' });
      
      this.captureState({
        frontend: { 
          route: '/settings',
          component: 'SettingsPage',
          state: { 
            user: { id: 123, status: 'active' },
            // Settings page triggers prefetch of user data
            prefetchedData: { messages: null }  // Not loaded yet
          }
        },
        cacheService: {
          keys: ['session:456', 'user:123', 'settings:123'],
          values: {
            'session:456': { userId: 123 },
            'user:123': { id: 123, status: 'active' },
            'settings:123': { theme: 'dark', notifications: true }
            // Still no messages in cache
          }
        }
      });
    });
    
    // Step 6: Return to chat view (BUG MANIFESTS HERE)
    await this.step(6, "VIEW_CHATS_BUG", async () => {
      const response = await this.mockableRequest('/api/messages/list', {
        method: 'GET',
        headers: { 'Authorization': 'Bearer new-token-456' }
      });
      
      // The bug: Cache key collision or state mismatch
      this.captureState({
        messageService: {
          messages: [],  // BUG: Returns empty due to cache key mismatch
          metadata: { 
            cacheKey: 'messages:123:active',  // New cache key format after status change
            expectedKey: 'messages:123',  // Original cache key
            cacheMiss: true
          }
        },
        frontend: {
          route: '/chat',
          component: 'ChatView', 
          state: {
            messages: [],  // Empty array shown to user
            error: null,  // No error reported
            loading: false
          }
        }
      });
      
      // This verification will FAIL, exposing the bug
      this.verify("Messages should be visible", (state) => 
        state.messageService.messages.length > 0
      );
    });
  }
  
  async binarySearchStateCorruption(): Promise<number> {
    console.log("🔬 Starting binary search for state corruption point");
    
    let left = 0;
    let right = this.stateHistory.length - 1;
    
    while (left < right) {
      const mid = Math.floor((left + right) / 2);
      console.log(`Checking state at step ${mid}`);
      
      // Reset and replay to midpoint
      await this.resetState();
      await this.replayToStep(mid);
      
      // Check if messages are still accessible
      const isValid = await this.checkMessageIntegrity();
      
      if (isValid) {
        console.log(`✅ State valid at step ${mid}, corruption is later`);
        left = mid + 1;
      } else {
        console.log(`❌ State corrupted at step ${mid}, corruption is earlier`);  
        right = mid;
      }
    }
    
    const corruptionStep = this.stateHistory[left];
    console.log(`🎯 State corruption occurs at step ${left}: ${corruptionStep.action}`);
    console.log("Corruption details:", this.analyzeStateTransition(left - 1, left));
    
    return left;
  }
  
  private analyzeStateTransition(beforeIdx: number, afterIdx: number): any {
    const before = this.stateHistory[beforeIdx];
    const after = this.stateHistory[afterIdx];
    
    return {
      action: after.action,
      stateChanges: {
        cacheKeyFormat: this.detectCacheKeyChange(before, after),
        sessionChange: this.detectSessionChange(before, after),
        userStatusChange: this.detectUserStatusChange(before, after)
      },
      hypothesis: this.generateHypothesis(before, after)
    };
  }
  
  private generateHypothesis(before: StateSnapshot, after: StateSnapshot): string {
    // Analyze the state transition to generate hypothesis
    if (before.services.authService.user?.status !== after.services.authService.user?.status) {
      return "User status change triggers different cache key pattern, causing cache miss";
    }
    if (before.services.authService.session?.token !== after.services.authService.session?.token) {
      return "New session token doesn't have access to previous session's cached data";
    }
    return "Unknown state corruption - requires deeper investigation";
  }
  
  // Helper methods
  private async step(num: number, action: string, fn: () => Promise<void>): Promise<void> {
    console.log(`\n📍 Step ${num}: ${action}`);
    await fn();
  }
  
  private captureState(partial: Partial<StateSnapshot['services']>): void {
    const snapshot: StateSnapshot = {
      timestamp: Date.now(),
      step: this.stateHistory.length + 1,
      action: '',
      services: { ...this.getCurrentState(), ...partial },
      networkCalls: []
    };
    this.stateHistory.push(snapshot);
  }
  
  private verify(description: string, verifier: (state: any) => boolean): void {
    const currentState = this.getCurrentState();
    const passed = verifier(currentState);
    console.log(`  ${passed ? '✅' : '❌'} ${description}`);
    if (!passed) {
      console.log("  State dump:", JSON.stringify(currentState, null, 2));
    }
  }
  
  private async mockableRequest(url: string, options: any): Promise<any> {
    // In test mode, return mocked responses
    // In debug mode, make real requests but log everything
    const mockKey = `${options.method}:${url}`;
    if (this.mockResponses.has(mockKey)) {
      return this.mockResponses.get(mockKey);
    }
    // Make real request and capture response
    return fetch(url, options);
  }
  
  private getCurrentState(): any {
    return this.stateHistory[this.stateHistory.length - 1]?.services || {};
  }
  
  private async resetState(): Promise<void> {
    // Reset all services to initial state
    this.stateHistory = [];
    this.mockResponses.clear();
  }
  
  private async replayToStep(step: number): Promise<void> {
    // Replay state sequence up to specified step
    for (let i = 0; i <= step && i < this.stateHistory.length; i++) {
      // Replay each state transition
      console.log(`Replaying step ${i}: ${this.stateHistory[i].action}`);
    }
  }
  
  private async checkMessageIntegrity(): Promise<boolean> {
    // Check if messages are accessible in current state
    const state = this.getCurrentState();
    return state.messageService?.messages?.length > 0 ||
           state.cacheService?.values?.['messages:123']?.length > 0;
  }
  
  private detectCacheKeyChange(before: StateSnapshot, after: StateSnapshot): any {
    const beforeKeys = before.services.cacheService?.keys || [];
    const afterKeys = after.services.cacheService?.keys || [];
    return {
      removed: beforeKeys.filter(k => !afterKeys.includes(k)),
      added: afterKeys.filter(k => !beforeKeys.includes(k))
    };
  }
  
  private detectSessionChange(before: StateSnapshot, after: StateSnapshot): any {
    return {
      before: before.services.authService?.session,
      after: after.services.authService?.session,
      changed: before.services.authService?.session?.token !== 
               after.services.authService?.session?.token
    };
  }
  
  private detectUserStatusChange(before: StateSnapshot, after: StateSnapshot): any {
    return {
      before: before.services.authService?.user?.status,
      after: after.services.authService?.user?.status,
      changed: before.services.authService?.user?.status !== 
               after.services.authService?.user?.status
    };
  }
}

// Usage
async function debugStatefulBug() {
  const harness = new StateReproductionHarness();
  
  // First, reproduce the full sequence to confirm bug
  await harness.executeStateSequence();
  
  // Then use binary search to find exact corruption point
  const corruptionStep = await harness.binarySearchStateCorruption();
  
  console.log(`\n🔍 Root Cause Analysis:`);
  console.log(`The bug occurs at step ${corruptionStep}`);
  console.log(`Issue: Cache key pattern changes when user status changes from 'pending' to 'active'`);
  console.log(`Solution: Ensure consistent cache key format regardless of user status`);
}
```

## Key Insights

1. **State Dependencies**: The bug only manifests when specific state sequences occur (signup → logout → login → settings → chat)

2. **Cache Key Mismatch**: The root cause is that cache keys change format when user status changes from 'pending' to 'active'

3. **Binary Search Effectiveness**: By checking state validity at midpoints, we quickly narrow from 6 steps to the exact transition causing corruption

4. **Service Isolation**: Each service in the chain must be tested independently while maintaining state consistency

## Fix Implementation

```typescript
// The fix: Use consistent cache keys
class MessageService {
  private getCacheKey(userId: number): string {
    // BUG: Was including user status in cache key
    // return `messages:${userId}:${userStatus}`;
    
    // FIX: Use consistent key format
    return `messages:${userId}`;
  }
}
```

## Verification

After applying the fix, run the complete state sequence again to ensure:
1. Messages persist across login/logout cycles
2. Cache keys remain consistent
3. No state corruption at any step