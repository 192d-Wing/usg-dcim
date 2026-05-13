// Branding for the login page. Edit this file to re-skin the login
// without touching component code or CSS. Text fields appear verbatim;
// color fields are exposed to globals.css as CSS custom properties on
// the .login-shell element.

// To swap the logo, drop a file into src/logo/ and update the import
// below. Vite bundles the asset and rewrites the URL at build time.
import dowSealBlue from '@/logo/DOW-Seal-Blue.png';

export type LoginLogo = {
  /** Resolved URL — import the asset above and pass the binding. */
  src: string;
  /** Alt text for screen readers. */
  alt: string;
  /** Rendered height in pixels. Width auto-scales. Default 160. */
  height?: number;
};

export type LoginSso = {
  /** Render the SSO button. Hide entirely when false. */
  enabled: boolean;
  /** Button label, e.g. "Login using DOD E-ICAM". */
  label: string;
  /**
   * URL the browser is sent to when the button is clicked. The
   * backend at this path is expected to 302 to the IdP. Relative
   * paths are resolved against the current origin.
   */
  loginUrl: string;
};

export type LoginBranding = {
  /** Hero name centered in the brand panel. */
  productName: string;
  /** Logo image rendered under the product name. Null hides it. */
  logo: LoginLogo | null;
  /** Large heading on the brand panel. */
  headline: string;
  /** Paragraph below the headline. Empty string hides it. */
  tagline: string;
  /** Small uppercase line at the bottom-left of the brand panel. */
  meta: string;
  /** Heading above the form. */
  formTitle: string;
  /** Description below the form heading. */
  formSubtitle: string;
  /** Footer note centered below the sign-in button. */
  footerNote: string;

  /** Single sign-on button shown above the email/password form. */
  sso: LoginSso;

  /**
   * Cloudscape component theme while the login page is mounted.
   * 'dark' renders inputs/buttons against a dark surface; 'light'
   * against a light one. Reverts to the app default on unmount.
   */
  cloudscapeMode: 'dark' | 'light';

  /** Hex colors. Wired into globals.css via CSS custom properties. */
  colors: {
    /** Brand accent — dot, divider strip, gradient highlights. */
    primary: string;
    /** Brand panel gradient: outer / mid / inner stops. */
    brandBgStart: string;
    brandBgMid: string;
    brandBgEnd: string;
    /** Form panel gradient: top / bottom stops. */
    formBgStart: string;
    formBgEnd: string;
    /** Body text color on both panels. */
    text: string;
    /** Heading color on the form panel. */
    formHeading: string;
  };
};

export const loginBranding: LoginBranding = {
  productName: 'Department of War DCIM',
  logo: {
    src: dowSealBlue,
    alt: 'Department of War seal',
    height: 160,
  },
  headline: 'Operate your fleet at every scale.',
  tagline:
    'Unified inventory, capacity, and observability for enterprise ' +
    'data-center operations — from a single rack to a global footprint.',
  meta: 'v0.2 · Department of the Air Force Enterprise Instance',
  formTitle: 'Welcome back',
  formSubtitle:
    'Sign in to continue.',
  footerNote: 'Trouble signing in? Contact your system administrator.',

  sso: {
    enabled: true,
    label: 'Login using DOD E-ICAM',
    // Backend handler at /api/v1/auth/oidc/login issues the 302 to
    // Keycloak using DCIM_OIDC_* env vars. nginx proxies /api/ to api.
    loginUrl: '/api/v1/auth/oidc/login',
  },

  cloudscapeMode: 'dark',

  // DOW Primary Blue (#355E93, PMS 653 C) anchors the palette.
  colors: {
    primary: '#355E93',
    brandBgStart: '#07101F',
    brandBgMid: '#0F1D36',
    brandBgEnd: '#16294A',
    formBgStart: '#0D1830',
    formBgEnd: '#0A1220',
    text: '#E4ECF6',
    formHeading: '#FFFFFF',
  },
};
