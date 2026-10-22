export interface ConfigTab {
  id: string
  label: string
  hasBadge?: boolean
}

interface ConfigPanelTabBarProps {
  tabs: ConfigTab[]
  activeTab: string
  onTabChange: (tabId: string) => void
}

export function ConfigPanelTabBar({ tabs, activeTab, onTabChange }: ConfigPanelTabBarProps) {
  return (
    <div className="cpv2-tab-bar">
      {tabs.map((tab) => (
        <button
          key={tab.id}
          type="button"
          className={activeTab === tab.id ? 'active' : ''}
          onClick={() => onTabChange(tab.id)}
        >
          {tab.label}
          {tab.hasBadge && <span className="cpv2-tab-badge" />}
        </button>
      ))}
    </div>
  )
}
