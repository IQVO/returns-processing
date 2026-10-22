import { Card, DataTable } from "@warehouse/ui-kit";
import { getDispositionPolicy } from "../api";
import { Facts, GradePill, OutcomePill, PageHeader, Stack } from "../components/layout";
import { EmptyNote, RequestView } from "../components/ProblemAlert";
import { useRequest } from "../hooks/useRequest";
import { utcLabel } from "../lib";
import type { DispositionPolicy } from "../types";

/**
 * The current disposition policy: GET /disposition-policies/current. The
 * (grade × productClass) table decides what happens to every inspected line;
 * the "*" row of a grade is its default, which makes Decide total. Read-only
 * view: policy versions are declared through the API (PUT
 * /disposition-policies/{version}) and are append-only -- nothing here edits
 * history.
 */
export function PolicyScreen() {
  const policy = useRequest(() => getDispositionPolicy(), "policy|current");
  // "No policy was ever declared" is the one 404 that is a normal state, not
  // a failure; render it as the empty screen it is.
  if (policy.status === "error" && policy.error.slug === "policy-not-found") {
    return (
      <Stack gap={5}>
        <PageHeader
          title="Disposition policy"
          subtitle="returns-processing · what happens to an inspected line, by grade and product class"
        />
        <Card title="No policy declared">
          <EmptyNote>
            No disposition policy version exists yet. Declare version 1 (PUT
            /disposition-policies/1) and every inspected line gets its outcome from it.
          </EmptyNote>
        </Card>
      </Stack>
    );
  }
  return (
    <Stack gap={5}>
      <PageHeader
        title="Disposition policy"
        subtitle="returns-processing · what happens to an inspected line, by grade and product class"
      />
      <RequestView state={policy} what="disposition policy">
        {(p) => <PolicyBody policy={p} />}
      </RequestView>
    </Stack>
  );
}

function PolicyBody({ policy }: { policy: DispositionPolicy }) {
  return (
    <>
      <Card title={`Version ${policy.version}`}>
        <Facts
          items={[
            { label: "Return window", value: `${policy.returnWindowDays} days after authorization` },
            { label: "Max units per line", value: String(policy.maxUnitsPerLine) },
            ...(policy.vetoOutcome ? [{ label: "Veto fallback", value: <OutcomePill outcome={policy.vetoOutcome} /> }] : []),
            { label: "Declared", value: utcLabel(policy.declaredAt) },
          ]}
        />
      </Card>
      <Card title="Rules">
        <DataTable
          rowKey={(r) => `${r.grade}/${r.productClass}`}
          rows={policy.rules}
          columns={[
            { key: "grade", header: "Grade", render: (r) => <GradePill grade={r.grade} /> },
            {
              key: "class",
              header: "Product class",
              render: (r) => (r.productClass === "*" ? "* (any — the grade default)" : r.productClass),
            },
            { key: "outcome", header: "Outcome", render: (r) => <OutcomePill outcome={r.outcome} /> },
          ]}
        />
        <EmptyNote>Versions are append-only; this view always shows the highest declared one.</EmptyNote>
      </Card>
    </>
  );
}
