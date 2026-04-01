const { expect } = require('chai');
const sinon = require('sinon');
const WindowManager = require('../src/window-manager');
const { BrowserWindow, ipcMain, app } = require('electron');

describe('WindowManager', () => {
  let windowManager;
  let mockWindow;
  let sandbox;

  beforeEach(() => {
    sandbox = sinon.createSandbox();
    
    // Mock BrowserWindow
    mockWindow = {
      id: 1,
      webContents: {
        send: sandbox.stub(),
        once: sandbox.stub(),
        setWindowOpenHandler: sandbox.stub(),
        on: sandbox.stub(),
        openDevTools: sandbox.stub(),
      },
      loadURL: sandbox.stub().resolves(),
      loadFile: sandbox.stub().resolves(),
      once: sandbox.stub(),
      on: sandbox.stub(),
      show: sandbox.stub(),
      focus: sandbox.stub(),
      isFocused: sandbox.stub().returns(false),
      isDestroyed: sandbox.stub().returns(false),
      setTitle: sandbox.stub(),
      close: sandbox.stub(),
    };

    sandbox.stub(BrowserWindow.prototype, 'constructor').returns(mockWindow);
    
    windowManager = new WindowManager();
  });

  afterEach(() => {
    sandbox.restore();
  });

  describe('createWindow', () => {
    it('should create a window with default options', async () => {
      const window = await windowManager.createWindow();
      
      expect(window).to.equal(mockWindow);
      expect(windowManager.windows.size).to.equal(1);
    });

    it('should use loadURL in development (app not packaged)', async () => {
      sandbox.stub(app, 'isPackaged').value(false);
      
      const window = await windowManager.createWindow();
      
      expect(mockWindow.loadURL.called).to.be.true;
      expect(mockWindow.loadFile.called).to.be.false;
    });

    it('should use loadFile in production (app packaged)', async () => {
      sandbox.stub(app, 'isPackaged').value(true);
      
      const window = await windowManager.createWindow();
      
      expect(mockWindow.loadFile.called).to.be.true;
      expect(mockWindow.loadURL.called).to.be.false;
    });

    it('should create a window with worktree context', async () => {
      const options = {
        worktreeId: 'wt-123',
        projectId: 'proj-456',
        projectName: 'Test Project',
      };

      const window = await windowManager.createWindow(options);
      
      expect(window).to.equal(mockWindow);
      expect(windowManager.windows.size).to.equal(1);
      expect(windowManager.worktreeWindows.has('wt-123')).to.be.true;
      
      const metadata = windowManager.getWindowMetadata(mockWindow.id);
      expect(metadata.worktreeId).to.equal('wt-123');
      expect(metadata.projectId).to.equal('proj-456');
      expect(metadata.projectName).to.equal('Test Project');
    });

    it('should send worktree context on window load', async () => {
      const options = {
        worktreeId: 'wt-123',
        projectId: 'proj-456',
        projectName: 'Test Project',
      };

      await windowManager.createWindow(options);
      
      // Simulate window finish loading
      const didFinishLoadCallback = mockWindow.webContents.once.getCall(0).args[1];
      didFinishLoadCallback();
      
      expect(mockWindow.webContents.send.calledWith('set-worktree-context')).to.be.true;
      const sentData = mockWindow.webContents.send.getCall(0).args[1];
      expect(sentData.worktreeId).to.equal('wt-123');
      expect(sentData.projectId).to.equal('proj-456');
    });
  });

  describe('openWorktreeWindow', () => {
    it('should create new window for worktree', async () => {
      const worktreeData = {
        id: 'wt-789',
        project_id: 'proj-111',
        name: 'Feature Branch',
        branch: 'feature/test',
      };

      const window = await windowManager.openWorktreeWindow(worktreeData);
      
      expect(window).to.equal(mockWindow);
      expect(windowManager.worktreeWindows.has('wt-789')).to.be.true;
    });

    it('should focus existing window if worktree already open', async () => {
      const worktreeData = {
        id: 'wt-789',
        project_id: 'proj-111',
        name: 'Feature Branch',
        branch: 'feature/test',
      };

      // First open
      await windowManager.openWorktreeWindow(worktreeData);
      
      // Second open should focus existing
      const window2 = await windowManager.openWorktreeWindow(worktreeData);
      
      expect(window2).to.equal(mockWindow);
      expect(mockWindow.focus.calledOnce).to.be.true;
      expect(windowManager.windows.size).to.equal(1);
    });
  });

  describe('window-worktree association', () => {
    it('should track worktree-window mapping', async () => {
      await windowManager.createWindow({ worktreeId: 'wt-1' });
      await windowManager.createWindow({ worktreeId: 'wt-2' });
      
      expect(windowManager.worktreeWindows.size).to.equal(2);
      expect(windowManager.worktreeWindows.get('wt-1')).to.exist;
      expect(windowManager.worktreeWindows.get('wt-2')).to.exist;
    });

    it('should clean up mapping on window close', async () => {
      await windowManager.createWindow({ worktreeId: 'wt-1' });
      
      // Simulate window close
      const closeCallback = mockWindow.on.getCall(0).args[1];
      closeCallback();
      
      expect(windowManager.windows.size).to.equal(0);
      expect(windowManager.worktreeWindows.has('wt-1')).to.be.false;
    });
  });

  describe('getAllWindows', () => {
    it('should return all window metadata', async () => {
      await windowManager.createWindow({ 
        worktreeId: 'wt-1',
        projectName: 'Project 1' 
      });
      await windowManager.createWindow({ 
        worktreeId: 'wt-2',
        projectName: 'Project 2' 
      });
      
      const windows = windowManager.getAllWindows();
      
      expect(windows).to.have.lengthOf(2);
      expect(windows[0].worktreeId).to.equal('wt-1');
      expect(windows[1].worktreeId).to.equal('wt-2');
    });
  });

  describe('broadcastToAllWindows', () => {
    it('should send message to all windows', async () => {
      const mockWindow2 = { ...mockWindow, id: 2 };
      
      await windowManager.createWindow();
      windowManager.windows.set(2, {
        id: 2,
        window: mockWindow2,
      });
      
      windowManager.broadcastToAllWindows('test-event', { data: 'test' });
      
      expect(mockWindow.webContents.send.calledWith('test-event')).to.be.true;
      expect(mockWindow2.webContents.send.calledWith('test-event')).to.be.true;
    });
  });

  describe('IPC handlers', () => {
    it('should handle get-window-context', async () => {
      const handler = sandbox.stub();
      ipcMain.handle = handler;
      
      windowManager.setupIpcHandlers();
      
      expect(handler.calledWith('get-window-context')).to.be.true;
    });

    it('should handle switch-worktree', async () => {
      const handler = sandbox.stub();
      ipcMain.handle = handler;
      
      windowManager.setupIpcHandlers();
      
      expect(handler.calledWith('switch-worktree')).to.be.true;
    });
  });
});