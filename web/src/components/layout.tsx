import type { ReactNode } from "react";
import { StatusPill } from "@warehouse/ui-kit";
import type { Grade, Outcome, ReturnStatus } from "../types";

export function PageHeader({ title, subtitle }: { title: string; subtitle: ReactNode }) {
  return (
    <div>
      <h1 style={{ fontSize: "var(--wh-font-size-2xl)", margin: 0 }}>{title}</h1>
      <p style={{ color: "var(--wh-color-text-muted)", marginTop: 4 }}>{subtitle}</p>
    </div>
  );
}

export function Stack({ children, gap = 4 }: { children: ReactNode; gap?: 3 | 4 | 5 }) {
  return (
    <div style={{ display: "flex", flexDirection: "column", gap: `var(--wh-space-${gap})` }}>
      {children}
    </div>
  );
}

/** A label/value list (dl) for a record's scalar fields. */
export function Facts({ items }: { items: { label: string; value: ReactNode }[] }) {
  return (
    <dl
      style={{
        display: "grid",
        gridTemplateColumns: "max-content 1fr",
        gap: "var(--wh-space-2) var(--wh-space-4)",
        margin: 0,
        fontSize: "var(--wh-font-size-sm)",
      }}
    >
      {items.map((item) => (
        <div key={item.label} style={{ display: "contents" }}>
          <dt style={{ color: "var(--wh-color-text-muted)" }}>{item.label}</dt>
          <dd style={{ margin: 0 }}>{item.value}</dd>
        </div>
      ))}
    </dl>
  );
}

type Tone = "neutral" | "progress" | "success" | "warning" | "danger";

/** These lifecycles are returns-processing's own, so StatusPill's tone
 *  override carries them. */
const STATUS_TONE: Record<ReturnStatus, Tone> = {
  REQUESTED: "progress",
  AUTHORIZED: "progress",
  REJECTED: "danger",
  RECEIVED: "warning",
  CLOSED: "success",
};

export function ReturnStatusPill({ status }: { status: ReturnStatus }) {
  return <StatusPill status={status} tone={STATUS_TONE[status]} size="sm" />;
}

const OUTCOME_TONE: Record<Outcome, Tone> = {
  RESTOCK: "success",
  REFURBISH: "progress",
  LIQUIDATE: "warning",
  DISPOSE: "danger",
  RTV: "neutral",
};

export function OutcomePill({ outcome }: { outcome: Outcome }) {
  return <StatusPill status={outcome} tone={OUTCOME_TONE[outcome]} size="sm" />;
}

export function GradePill({ grade }: { grade: Grade }) {
  return <StatusPill status={grade} tone={grade === "A" ? "success" : grade === "SCRAP" ? "danger" : "warning"} size="sm" />;
}
