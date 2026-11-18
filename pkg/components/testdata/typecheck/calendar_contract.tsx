// The Calendar's public type contract, checked by `tsc` in
// TestComponentTypeContracts (library_type_contract_test.go). Every
// `@ts-expect-error` below is a misuse the type must REJECT.
import React from "react";

import Calendar, { type CalendarItem, type CalendarView } from "./calendar";

const items: CalendarItem[] = [
  { id: "1", date: "2026-11-12", title: "Launch post", channel: "bluesky" },
  { id: "2", date: new Date(2026, 10, 13, 9, 30), title: "HN", channel: "hn", movable: false },
  { id: "3", date: "2026-11-14T16:00:00Z", title: "Newsletter", timeLabel: "4pm" },
];

export function Accepted() {
  const [view, setView] = React.useState<CalendarView>("month");
  const [date, setDate] = React.useState(new Date(2026, 10, 1));
  return (
    <>
      <Calendar items={items} />
      <Calendar
        items={items}
        channels={[
          { id: "bluesky", label: "Bluesky", color: "#1185fe" },
          { id: "hn", label: "HN" },
        ]}
        view={view}
        onViewChange={setView}
        date={date}
        onDateChange={setDate}
        onMove={(itemId: string, newDate: Date) => void [itemId, newDate]}
        onDayClick={(day: Date) => void day}
        onItemClick={(item) => void item.id}
        weekStartsOn={1}
        locale="en-GB"
        maxItemsPerDay={2}
        today={new Date(2026, 10, 12)}
      />
    </>
  );
}

export function Rejected() {
  return (
    <>
      {/* @ts-expect-error onMove's new date is a Date, not a string */}
      <Calendar items={items} onMove={(id: string, date: string) => void [id, date]} />
      {/* @ts-expect-error there is no "day" view */}
      <Calendar items={items} defaultView="day" />
      {/* @ts-expect-error an item needs a title */}
      <Calendar items={[{ id: "x", date: "2026-11-12" }]} />
      {/* @ts-expect-error a week starts on Sunday or Monday */}
      <Calendar items={items} weekStartsOn={3} />
    </>
  );
}
