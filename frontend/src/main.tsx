import React from 'react';
import ReactDOM from 'react-dom/client';
import { App } from './App';
// Cloudscape global stylesheet must load before our own utility CSS so
// our Tailwind utilities can still override it where they need to (the
// app shell is Cloudscape; page bodies still mix shadcn during the
// migration). Density stays at Comfortable (Cloudscape's default) —
// the Compact density we tried first cramped table rows hard enough to
// look broken; dense data still reads fine at standard row heights.
import '@cloudscape-design/global-styles/index.css';
import { applyDensity, applyMode, Density, Mode } from '@cloudscape-design/global-styles';
// Lato — self-hosted via @fontsource so the bundle doesn't depend on
// Google Fonts at runtime. Two weights cover body (400) and headings (700).
import '@fontsource/lato/400.css';
import '@fontsource/lato/700.css';
import './globals.css';

applyDensity(Density.Comfortable);
// Match Cloudscape's mode to Tailwind's. Our index.html ships with
// `class="dark"` on <html>, so the page background is dark; without
// this call, Cloudscape components render in light mode and look like
// white cards floating on a dark page.
applyMode(document.documentElement.classList.contains('dark') ? Mode.Dark : Mode.Light);

// Surface render-time errors as readable text instead of a blank screen so
// container deploys are diagnosable without DevTools. The boundary catches
// anything synchronous in the render tree below it; lazy chunk failures
// already surface via Suspense + native error events.
class RootErrorBoundary extends React.Component<
  { children: React.ReactNode },
  { error: Error | null }
> {
  state = { error: null as Error | null };
  static getDerivedStateFromError(error: Error) { return { error }; }
  componentDidCatch(error: Error, info: React.ErrorInfo) {
    // eslint-disable-next-line no-console
    console.error('Root render error:', error, info);
  }
  render() {
    if (!this.state.error) return this.props.children;
    return (
      <div style={{
        padding: 24, fontFamily: 'ui-sans-serif, system-ui',
        color: '#f5f5f5', background: '#0b0d10', minHeight: '100vh',
      }}>
        <h1 style={{ fontSize: 20, fontWeight: 600 }}>USG DCIM — render error</h1>
        <p style={{ color: '#fca5a5', marginTop: 8 }}>{this.state.error.message}</p>
        <pre style={{
          marginTop: 16, padding: 12, background: '#1a1d23',
          borderRadius: 6, fontSize: 12, overflowX: 'auto',
          whiteSpace: 'pre-wrap',
        }}>
          {this.state.error.stack}
        </pre>
        <p style={{ marginTop: 24, color: '#9ca3af', fontSize: 12 }}>
          Open DevTools console for the full trace. Try clearing localStorage
          keys <code>dcim.token</code> and <code>dcim.identity</code>, then hard-refresh.
        </p>
      </div>
    );
  }
}

// The boot-error handler in index.html may have written diagnostics into
// #root if a top-level module load failed earlier. We're past that now, so
// clear it before mounting React.
const rootEl = document.getElementById('root')!;
rootEl.innerHTML = '';

ReactDOM.createRoot(rootEl).render(
  <React.StrictMode>
    <RootErrorBoundary>
      <App />
    </RootErrorBoundary>
  </React.StrictMode>,
);
