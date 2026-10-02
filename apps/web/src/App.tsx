export function App() {
  return (
    <main className="page-shell">
      <header className="topbar">
        <a className="brand" href="/" aria-label="DiOffice home">
          <span className="brand-mark" aria-hidden="true">D</span>
          <span>DiOffice</span>
        </a>
        <span className="environment-label">LOCAL FOUNDATION</span>
      </header>

      <section className="hero" aria-labelledby="page-title">
        <p className="eyebrow">A real company, built one workflow at a time</p>
        <h1 id="page-title">Give good work a place to happen.</h1>
        <p className="intro">
          The application foundation is ready. Employee activity will appear here
          only when it is backed by persisted project state.
        </p>

        <div className="foundation-card" role="status" aria-live="polite">
          <span className="status-dot" aria-hidden="true" />
          <div>
            <strong>Project foundation</strong>
            <p>No task activity is running.</p>
          </div>
          <span className="card-note">No simulated progress</span>
        </div>

        <footer className="next-step">
          <span className="next-step-label">NEXT VERTICAL SLICE</span>
          <p>Persist an Owner-created task and its event history.</p>
        </footer>
      </section>
    </main>
  );
}
