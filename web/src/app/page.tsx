import styles from "./page.module.css";
import dynamic from "next/dynamic";

// Import ThemeToggle and CopyButton dynamically to prevent hydration issues
const ThemeToggle = dynamic(() => import("./components/ThemeToggle"), {
});

const CopyButton = dynamic(() => import("./components/CopyButton"), {
});

export default function Home() {
  return (
    <div className={styles.page}>
      <header className={styles.header}>
        <h1 className={styles.title}>atrium</h1>
        <div className={styles.headerActions}>
          <a
            className={styles.headerButton}
            href="https://github.com/ZviBaratz/atrium"
            target="_blank"
            rel="noopener noreferrer"
          >
            GitHub
          </a>
          <a
            href="https://github.com/ZviBaratz/atrium#readme"
            target="_blank"
            rel="noopener noreferrer"
            className={styles.headerButton}
          >
            Docs
          </a>
          <ThemeToggle />
        </div>
      </header>
      <main className={styles.main}>
        
        
        <p className={styles.tagline}>
          Manage multiple AI coding agents like <span className={styles.highlight}>Claude Code</span>, <span className={styles.highlight}>Codex</span>, <span className={styles.highlight}>Gemini CLI</span>, <span className={styles.highlight}>Aider</span>, and <span className={styles.highlight}>Antigravity</span>.
        </p>
        <p className={styles.subtagline}>
          Each agent works in its own isolated git worktree, so you can drive several tasks at once from a single panel.
        </p>

        <div className={styles.installation}>
          <h2>Installation</h2>
          <h3>Via Shell Script</h3>
          <div className={styles.codeBlockWrapper}>
            <pre className={styles.codeBlock}>
              <code>curl -fsSL https://raw.githubusercontent.com/ZviBaratz/atrium/main/install.sh | bash</code>
            </pre>
            <CopyButton textToCopy="curl -fsSL https://raw.githubusercontent.com/ZviBaratz/atrium/main/install.sh | bash" />
          </div>
          <br></br>
          <h3>Via go install</h3>
          <div className={styles.codeBlockWrapper}>
            <pre className={styles.codeBlock}>
              <code>go install github.com/ZviBaratz/atrium@latest</code>
            </pre>
            <CopyButton textToCopy="go install github.com/ZviBaratz/atrium@latest" />
          </div>
          <p className={styles.prerequisites}>
            Prerequisites: tmux 3.2+, git, gh (GitHub CLI, optional)
          </p>
        </div>
        
        <div className={styles.features}>
          <h2>Why use Atrium?</h2>
          <ul>
            <li>Supervise multiple agents in one UI</li>
            <li>Isolate tasks in git workspaces</li>
            <li>Review work before shipping</li>
          </ul>
        </div>
      </main>
      <footer className={styles.footer}>
        <p className={styles.copyright}>
          &copy; {new Date().getFullYear()} Atrium. Licensed under <a href="https://github.com/ZviBaratz/atrium/blob/main/LICENSE.md" target="_blank" rel="noopener noreferrer">GNU AGPL v3.0</a>
        </p>
      </footer>
    </div>
  );
}
