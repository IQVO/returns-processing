import { Navigate, NavLink, Outlet, Route, Routes } from "react-router-dom";
import { PolicyScreen } from "./screens/PolicyScreen";
import { ReturnsListScreen } from "./screens/ReturnsListScreen";
import { AuthorizeReturnScreen } from "./screens/AuthorizeReturnScreen";
import { ReturnDetailRoute } from "./screens/ReturnDetailScreen";

const SUB_NAV = [
  { to: "returns", label: "Returns", end: false },
  { to: "policy", label: "Disposition policy", end: false },
];

const linkStyle = ({ isActive }: { isActive: boolean }) => ({
  display: "inline-flex",
  padding: "6px 12px",
  borderRadius: "var(--wh-radius-pill)",
  fontSize: "var(--wh-font-size-sm)",
  fontWeight: isActive ? 600 : 500,
  color: isActive ? "var(--wh-color-text)" : "var(--wh-color-text-muted)",
  background: isActive ? "var(--wh-color-accent-muted)" : "transparent",
  textDecoration: "none",
});

/** The sub-nav, rendered by a layout route with the path "/".
 *
 *  The console mounts this component inside its own `<Route
 *  path="/returns-processing/*">`. A relative `<NavLink>` rendered straight
 *  in that splat route resolves against the FULL current URL (react-router 7
 *  uses the last path-contributing match's pathname, splat included), so from
 *  /returns-processing/policy the link `returns` would point at
 *  /returns-processing/policy/returns. A pathless layout route does not help
 *  (react-router drops pathless routes from relative resolution); a layout
 *  route WITH path "/" does: its own match is the mount point, under any
 *  prefix and standalone at `/`. (Same fix as the pick-face-replenishment,
 *  slotting and inbound-receiving remotes; App.test.tsx mounts under the
 *  host's splat route to prove it.) */
function ReturnsLayout() {
  return (
    <div style={{ display: "flex", flexDirection: "column", gap: "var(--wh-space-5)" }}>
      <nav aria-label="Returns processing sections" style={{ display: "flex", gap: "var(--wh-space-2)" }}>
        {SUB_NAV.map((item) => (
          <NavLink key={item.to} to={item.to} end={item.end} style={linkStyle}>
            {item.label}
          </NavLink>
        ))}
      </nav>
      <Outlet />
    </div>
  );
}

/** Exposed as returns_mfe/App via Module Federation. Takes NO props: the
 *  console mounts it under `/returns-processing/*` and provides the
 *  BrowserRouter, the design tokens and `window.__WAREHOUSE_CONFIG__`. Routes
 *  here are RELATIVE, so the component works identically under that prefix
 *  (in the shell) or at / (standalone dev, see main.tsx).
 *
 *   - (index): redirects to returns.
 *   - returns: the RMA list of one order (GET /returns?orderRef=).
 *   - returns/new: authorize a return (Idempotency-Key on the POST).
 *   - returns/:rmaId: one RMA; while AUTHORIZED, open the receipt, then
 *     inspect its lines and see (or override) the decided disposition
 *     (Idempotency-Key on every POST).
 *   - policy: the current disposition policy table (read-only view). */
export default function App() {
  return (
    <Routes>
      <Route path="/" element={<ReturnsLayout />}>
        <Route index element={<Navigate to="returns" replace />} />
        <Route path="returns" element={<ReturnsListScreen />} />
        <Route path="returns/new" element={<AuthorizeReturnScreen />} />
        <Route path="returns/:rmaId" element={<ReturnDetailRoute />} />
        <Route path="policy" element={<PolicyScreen />} />
      </Route>
    </Routes>
  );
}
