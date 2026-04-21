// Loads Firebase modular SDK on demand so the initial bundle stays small.
// Uses the same hard-coded API key the Windsurf web client ships with — the
// three alternatives in CLAUDE.md are confirmed non-functional.

const FIREBASE_CONFIG = {
  apiKey: 'AIzaSyDsOl-1XpT5err0Tcnx8FFod1H8gVGIycY',
  authDomain: 'exa2-fb170.firebaseapp.com',
  projectId: 'exa2-fb170',
} as const;

type Provider = 'google' | 'github';

export interface FirebaseOAuthPayload {
  idToken: string;
  refreshToken: string;
  email: string;
  provider: Provider;
}

interface FirebaseBundle {
  auth: unknown;
  signInWithPopup: (auth: unknown, provider: unknown) => Promise<{
    user: {
      email: string | null;
      getIdToken: () => Promise<string>;
      stsTokenManager?: { refreshToken?: string };
    };
  }>;
  GoogleAuthProvider: new () => { addScope: (s: string) => void };
  GithubAuthProvider: new () => { addScope: (s: string) => void };
}

let initPromise: Promise<FirebaseBundle> | null = null;

async function ensure(): Promise<FirebaseBundle> {
  if (!initPromise) {
    initPromise = (async () => {
      const appUrl = 'https://www.gstatic.com/firebasejs/11.6.0/firebase-app.js';
      const authUrl = 'https://www.gstatic.com/firebasejs/11.6.0/firebase-auth.js';
      const appMod = (await import(/* @vite-ignore */ appUrl)) as {
        initializeApp: (cfg: typeof FIREBASE_CONFIG) => unknown;
      };
      const authMod = (await import(/* @vite-ignore */ authUrl)) as {
        getAuth: (app: unknown) => unknown;
        signInWithPopup: FirebaseBundle['signInWithPopup'];
        GoogleAuthProvider: FirebaseBundle['GoogleAuthProvider'];
        GithubAuthProvider: FirebaseBundle['GithubAuthProvider'];
      };
      const app = appMod.initializeApp(FIREBASE_CONFIG);
      return {
        auth: authMod.getAuth(app),
        signInWithPopup: authMod.signInWithPopup,
        GoogleAuthProvider: authMod.GoogleAuthProvider,
        GithubAuthProvider: authMod.GithubAuthProvider,
      };
    })();
  }
  return initPromise;
}

export async function signInWithOAuth(provider: Provider): Promise<FirebaseOAuthPayload> {
  const mod = await ensure();
  const p = provider === 'google' ? new mod.GoogleAuthProvider() : new mod.GithubAuthProvider();
  if (provider === 'google') p.addScope('email');
  const result = await mod.signInWithPopup(mod.auth, p);
  const idToken = await result.user.getIdToken();
  return {
    idToken,
    refreshToken: result.user.stsTokenManager?.refreshToken ?? '',
    email: result.user.email ?? '',
    provider,
  };
}
