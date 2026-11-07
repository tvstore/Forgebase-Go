import { useState } from 'react';
import type { FormEvent } from 'react';

import { ApiError, login } from '../api';
import { Logo } from '../components/Logo';
import type { SessionInfo } from '../types';

interface Props {
  onLoggedIn: (session: SessionInfo) => void;
}

export function Login({ onLoggedIn }: Props) {
  const [username, setUsername] = useState('');
  const [password, setPassword] = useState('');
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  const submit = (event: FormEvent) => {
    event.preventDefault();
    if (busy) return;
    setBusy(true);
    setError(null);
    login(username, password).then(
      (session) => {
        setPassword('');
        onLoggedIn(session);
      },
      (e: unknown) => {
        setBusy(false);
        // The server's message is uniform ("invalid username or password"): shown as it is.
        setError(e instanceof ApiError ? e.message : 'Cannot reach the server. Try again.');
      },
    );
  };

  return (
    <main className="login">
      <div className="login-card">
        <div className="login-brand">
          <Logo size={48} />
          <h1>Forgebase</h1>
          <p className="subtitle">The foundation for business applications.</p>
        </div>
        <form onSubmit={submit}>
          {error ? (
            <p role="alert" className="alert">
              {error}
            </p>
          ) : null}
          <div className="field">
            <label htmlFor="username">Username</label>
            <input
              id="username"
              name="username"
              autoComplete="username"
              autoFocus
              required
              value={username}
              onChange={(e) => setUsername(e.target.value)}
            />
          </div>
          <div className="field">
            <label htmlFor="password">Password</label>
            <input
              id="password"
              name="password"
              type="password"
              autoComplete="current-password"
              required
              value={password}
              onChange={(e) => setPassword(e.target.value)}
            />
          </div>
          <button type="submit" className="btn btn-primary btn-block" aria-disabled={busy || undefined}>
            {busy ? 'Logging in...' : 'Log in'}
          </button>
        </form>
      </div>
    </main>
  );
}
