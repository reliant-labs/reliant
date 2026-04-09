import React from 'react';

interface ComparisonMatrixProps {
  /** Column headers (product/company names) */
  columns: Array<{
    name: string;
    /** Highlight this column */
    highlight?: boolean;
  }>;
  /** Row groups */
  groups: Array<{
    name: string;
    features: Array<{
      name: string;
      /** Values per column: true=check, false=cross, string=custom text */
      values: Array<boolean | string>;
    }>;
  }>;
}

function CheckIcon(): React.ReactElement {
  return (
    <svg width="20" height="20" viewBox="0 0 20 20" fill="none" className="text-emerald-500">
      <circle cx="10" cy="10" r="9" fill="currentColor" fillOpacity="0.12" />
      <path
        d="M6 10.5l2.5 2.5 5.5-5.5"
        stroke="currentColor"
        strokeWidth="2"
        strokeLinecap="round"
        strokeLinejoin="round"
      />
    </svg>
  );
}

function CrossIcon(): React.ReactElement {
  return (
    <svg width="20" height="20" viewBox="0 0 20 20" fill="none" className="text-red-400">
      <circle cx="10" cy="10" r="9" fill="currentColor" fillOpacity="0.08" />
      <path
        d="M7 7l6 6M13 7l-6 6"
        stroke="currentColor"
        strokeWidth="2"
        strokeLinecap="round"
      />
    </svg>
  );
}

function CellValue({ value }: { value: boolean | string }): React.ReactElement {
  if (value === true) return <CheckIcon />;
  if (value === false) return <CrossIcon />;
  return <span className="text-xs text-gray-600 font-medium">{value}</span>;
}

export default function ComparisonMatrix({
  columns,
  groups,
}: ComparisonMatrixProps): React.ReactElement {
  if (columns.length === 0) {
    return <div className="text-sm text-gray-400 italic">No columns provided</div>;
  }

  const totalCols = columns.length + 1; // +1 for the feature name column

  return (
    <div className="w-full overflow-x-auto">
      <table className="w-full border-collapse text-sm">
        {/* Column headers */}
        <thead>
          <tr>
            {/* Empty top-left cell */}
            <th className="sticky left-0 z-10 bg-white p-3 text-left min-w-[180px]" />
            {columns.map((col, ci) => (
              <th
                key={ci}
                className={`p-3 text-center font-bold text-sm min-w-[120px] ${
                  col.highlight
                    ? 'bg-blue-50 text-blue-700 border-x-2 border-t-2 border-blue-200 rounded-t-lg'
                    : 'text-gray-700'
                }`}
              >
                {col.highlight && (
                  <span className="block text-[10px] uppercase tracking-widest text-blue-500 font-semibold mb-0.5">
                    Recommended
                  </span>
                )}
                {col.name}
              </th>
            ))}
          </tr>
        </thead>

        <tbody>
          {groups.map((group, gi) => (
            <React.Fragment key={gi}>
              {/* Group header row */}
              <tr>
                <td
                  colSpan={totalCols}
                  className="px-3 pt-5 pb-2 text-xs font-bold uppercase tracking-wider text-gray-400 border-b border-gray-100"
                >
                  {group.name}
                </td>
              </tr>

              {/* Feature rows */}
              {group.features.map((feature, fi) => (
                <tr
                  key={fi}
                  className={fi % 2 === 0 ? 'bg-white' : 'bg-gray-50/50'}
                >
                  {/* Feature name */}
                  <td className="sticky left-0 z-10 bg-inherit px-3 py-2.5 text-sm text-gray-700 font-medium border-b border-gray-100">
                    {feature.name}
                  </td>

                  {/* Values */}
                  {columns.map((col, ci) => {
                    const value = feature.values[ci];
                    return (
                      <td
                        key={ci}
                        className={`px-3 py-2.5 text-center border-b border-gray-100 ${
                          col.highlight ? 'bg-blue-50/60 border-x-2 border-x-blue-200' : ''
                        }`}
                      >
                        <div className="flex items-center justify-center">
                          {value !== undefined ? (
                            <CellValue value={value} />
                          ) : (
                            <span className="text-gray-300">—</span>
                          )}
                        </div>
                      </td>
                    );
                  })}
                </tr>
              ))}
            </React.Fragment>
          ))}
        </tbody>
      </table>
    </div>
  );
}
