// Branding for the login page. Edit this file to re-skin the login
// without touching component code or CSS. Text fields appear verbatim;
// color fields are exposed to globals.css as CSS custom properties on
// the .login-shell element.

export type LoginBranding = {
  /** Wordmark shown next to the accent dot in the brand panel. */
  productName: string;
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
  productName: 'USG DCIM',
  headline: 'Operate your fleet at every scale.',
  tagline:
    'Unified inventory, capacity, and observability for enterprise ' +
    'data-center operations — from a single rack to a global footprint.',
  meta: 'v0.2 · enterprise edition',
  formTitle: 'Welcome back',
  formSubtitle:
    'Sign in to continue. Production deployments use OIDC/SAML; ' +
    'local dev accepts the seeded admin.',
  footerNote: 'Trouble signing in? Contact your system administrator.',

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
