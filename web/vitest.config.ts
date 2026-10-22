import { defineConfig } from "vitest/config";
import react from "@vitejs/plugin-react";

// Standalone on purpose: it does NOT import vite.config.ts or mergeConfig it.
// The Module Federation plugin has nothing to do in a unit test, and keeping
// the two files independent means no change to the app's Vite config (object
// or callback form) can ever take the test suite down with it.
export default defineConfig({
  plugins: [react()],
  resolve: {
    // @warehouse/ui-kit is consumed via `file:../../warehouse-ui-kit` and has
    // its own installed react/react-dom. Without dedupe, ui-kit components
    // call hooks against a SEPARATE React module instance than the one
    // rendering the test tree ("Invalid hook call").
    dedupe: ["react", "react-dom"],
  },
  test: {
    environment: "jsdom",
    globals: true,
    setupFiles: ["./src/test/setup.ts"],
    css: false,
    include: ["src/**/*.test.{ts,tsx}"],
  },
});
