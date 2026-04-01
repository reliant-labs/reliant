import { ConnectError, Code } from '@connectrpc/connect';
import { SkillConflictPolicy, SkillScope } from '../../gen/reliant/v1/settings_pb';
import { settingsGrpc } from '../settings-grpc';

const mocks = vi.hoisted(() => ({
  installSkill: vi.fn(),
  listInstalledSkills: vi.fn(),
  getInstalledSkillDefinition: vi.fn(),
  setSkillEnabled: vi.fn(),
  listRecommendedSkills: vi.fn(),
}));

vi.mock('../grpc-client', () => ({
  grpcClient: {
    settings: () => ({
      installSkill: mocks.installSkill,
      listInstalledSkills: mocks.listInstalledSkills,
      getInstalledSkillDefinition: mocks.getInstalledSkillDefinition,
      setSkillEnabled: mocks.setSkillEnabled,
      listRecommendedSkills: mocks.listRecommendedSkills,
      // Methods not used in this test file, but present on the client object
      createSetting: vi.fn(),
      listSettings: vi.fn(),
      getSetting: vi.fn(),
      updateSetting: vi.fn(),
      deleteSetting: vi.fn(),
      getShortcuts: vi.fn(),
      updateShortcuts: vi.fn(),
      getPreferences: vi.fn(),
      updatePreferences: vi.fn(),
      setHiddenItem: vi.fn(),
      getPrompts: vi.fn(),
      savePrompts: vi.fn(),
      getProviderStatuses: vi.fn(),
      updateProviderAPIKey: vi.fn(),
      validateProviderAPIKey: vi.fn(),
      completeCodexOAuth: vi.fn(),
      getPrivacySettings: vi.fn(),
      updatePrivacySettings: vi.fn(),
      trackPageVisited: vi.fn(),
      deleteGlobalSkill: vi.fn(),
      getConfigHealth: vi.fn(),
    }),
  },
}));

describe('settings-grpc skills contract mappings', () => {
  beforeEach(() => {
    mocks.installSkill.mockReset();
    mocks.listInstalledSkills.mockReset();
    mocks.getInstalledSkillDefinition.mockReset();
    mocks.setSkillEnabled.mockReset();
    mocks.listRecommendedSkills.mockReset();
  });

  it('installSkill maps typed enum request values and typed result response', async () => {
    mocks.installSkill.mockResolvedValue({
      success: true,
      message: 'ok',
      result: {
        source: 'https://github.com/acme/skills.git',
        sourceType: 2,
        sourceSubpath: 'skills/sql-debug',
        gitRef: 'main',
        resolvedSource: '/tmp/source',
        targetDir: '/tmp/project/.reliant/skills/sql-debug',
        skillName: 'sql-debug',
        installDirName: 'sql-debug',
        installedFiles: ['SKILL.md', 'guide.md'],
        skippedFiles: ['README.md'],
        dryRun: false,
        scope: SkillScope.PROJECT,
        conflictPolicy: SkillConflictPolicy.OVERWRITE,
      },
    });

    const result = await settingsGrpc.installSkill({
      project_id: 'project-1',
      source: 'https://github.com/acme/skills.git',
      source_subpath: 'skills/sql-debug',
      ref: 'main',
      scope: 'project',
      conflict_policy: 'overwrite',
      dry_run: false,
    });

    expect(result.success).toBe(true);
    expect(result.result?.source_type).toBe('git');
    expect(result.result?.scope).toBe('project');
    expect(result.result?.conflict_policy).toBe('overwrite');
    expect(result.result?.installed_files).toEqual(['SKILL.md', 'guide.md']);

    const request = mocks.installSkill.mock.calls[0][0];
    expect(request.projectId).toBe('project-1');
    expect(request.scope).toBe(SkillScope.PROJECT);
    expect(request.conflictPolicy).toBe(SkillConflictPolicy.OVERWRITE);
  });

  it('installSkill returns deterministic disabled response when feature gate is off', async () => {
    mocks.installSkill.mockRejectedValue(
      new ConnectError('skills feature is disabled', Code.FailedPrecondition)
    );

    const result = await settingsGrpc.installSkill({
      project_id: 'project-1',
      source: 'https://github.com/acme/skills.git',
      dry_run: false,
    });

    expect(result).toEqual({
      success: false,
      message: 'Skills feature is disabled',
    });
  });

  it('listInstalledSkills forwards pagination and maps next_page_token + skill_id', async () => {
    mocks.listInstalledSkills.mockResolvedValue({
      skills: [
        {
          skillId: 'builtin|builtin/skill-creator/SKILL.md|active',
          name: 'skill-creator',
          description: 'Create skills',
          scope: SkillScope.BUILTIN,
          format: 1,
          skillDir: 'builtin/skill-creator',
          definitionPath: 'builtin/skill-creator/SKILL.md',
          active: true,
          shadowedByDefinitionPath: '',
        },
      ],
      total: 1,
      diagnostics: [
        {
          path: '/tmp/project/.reliant/skills/broken/SKILL.md',
          scope: SkillScope.PROJECT,
          message: 'invalid frontmatter',
        },
      ],
      nextPageToken: '100',
    });

    const result = await settingsGrpc.listInstalledSkills('project-1', {
      page_size: 50,
      page_token: '0',
    });

    expect(result.total).toBe(1);
    expect(result.next_page_token).toBe('100');
    expect(result.skills[0].skill_id).toContain('|active');
    expect(result.skills[0].scope).toBe('builtin');
    expect(result.diagnostics[0].scope).toBe('project');

    const request = mocks.listInstalledSkills.mock.calls[0][0];
    expect(request.projectId).toBe('project-1');
    expect(request.pageSize).toBe(50);
    expect(request.pageToken).toBe('0');
  });

  it('listInstalledSkills returns empty deterministic payload when feature gate is off', async () => {
    mocks.listInstalledSkills.mockRejectedValue(
      new ConnectError('skills feature is disabled', Code.FailedPrecondition)
    );

    const result = await settingsGrpc.listInstalledSkills('project-1', {
      page_size: 50,
      page_token: '0',
    });

    expect(result).toEqual({
      skills: [],
      total: 0,
      diagnostics: [],
      next_page_token: '',
    });
  });

  it('getInstalledSkillDefinition maps skill_id/detail payload', async () => {
    mocks.getInstalledSkillDefinition.mockResolvedValue({
      skillId: 'project|/tmp/project/.reliant/skills/sql-debug/SKILL.md|active',
      definitionPath: '/tmp/project/.reliant/skills/sql-debug/SKILL.md',
      definitionContent: '# skill body',
      assets: [
        {
          path: 'assets/logo.png',
          mimeType: 'image/png',
          content: new Uint8Array([1, 2, 3]),
        },
      ],
    });

    const result = await settingsGrpc.getInstalledSkillDefinition(
      'project-1',
      'project|/tmp/project/.reliant/skills/sql-debug/SKILL.md|active',
    );

    expect(result.skill_id).toContain('sql-debug');
    expect(result.definition_path).toContain('SKILL.md');
    expect(result.definition_content).toContain('skill body');
    expect(result.assets).toHaveLength(1);
    expect(result.assets[0].path).toBe('assets/logo.png');
    expect(result.assets[0].mime_type).toBe('image/png');
    expect(Array.from(result.assets[0].content)).toEqual([1, 2, 3]);

    const request = mocks.getInstalledSkillDefinition.mock.calls[0][0];
    expect(request.projectId).toBe('project-1');
    expect(request.skillId).toContain('sql-debug');
  });

  it('setSkillEnabled maps request/response payload', async () => {
    mocks.setSkillEnabled.mockResolvedValue({
      success: true,
      message: 'Skill disabled',
      skillId: 'project|/tmp/project/.reliant/skills/sql-debug/SKILL.md|active',
      enabled: false,
    });

    const result = await settingsGrpc.setSkillEnabled(
      'project-1',
      'project|/tmp/project/.reliant/skills/sql-debug/SKILL.md|active',
      false,
    );

    expect(result.success).toBe(true);
    expect(result.message).toBe('Skill disabled');
    expect(result.skill_id).toContain('sql-debug');
    expect(result.enabled).toBe(false);

    const request = mocks.setSkillEnabled.mock.calls[0][0];
    expect(request.projectId).toBe('project-1');
    expect(request.skillId).toContain('sql-debug');
    expect(request.enabled).toBe(false);
  });

  it('listRecommendedSkills forwards pagination and maps next_page_token', async () => {
    mocks.listRecommendedSkills.mockResolvedValue({
      recommended: [
        {
          id: 'sql-debug',
          name: 'SQL Debug',
          description: 'Debug SQL queries',
          source: 'https://github.com/acme/skills.git',
          sourceSubpath: 'skills/sql-debug',
          ref: 'main',
          bundledBy: 'acme',
        },
      ],
      total: 1,
      nextPageToken: '1',
    });

    const result = await settingsGrpc.listRecommendedSkills('project-1', {
      page_size: 25,
      page_token: '0',
    });

    expect(result.total).toBe(1);
    expect(result.next_page_token).toBe('1');
    expect(result.recommended[0].id).toBe('sql-debug');

    const request = mocks.listRecommendedSkills.mock.calls[0][0];
    expect(request.projectId).toBe('project-1');
    expect(request.pageSize).toBe(25);
    expect(request.pageToken).toBe('0');
  });

  it('listRecommendedSkills returns empty deterministic payload when feature gate is off', async () => {
    mocks.listRecommendedSkills.mockRejectedValue(
      new ConnectError('skills feature is disabled', Code.FailedPrecondition)
    );

    const result = await settingsGrpc.listRecommendedSkills('project-1', {
      page_size: 25,
      page_token: '0',
    });

    expect(result).toEqual({
      recommended: [],
      total: 0,
      next_page_token: '',
    });
  });
});
