# Send Email Hook

Custom auth email templates using React Email and Resend.

## Overview

This Edge Function intercepts all Supabase Auth emails (signup, magic link, password reset, etc.) and sends them via Resend with custom React Email templates.

```
User Action → Supabase Auth → Send Email Hook → This Function → React Email → Resend API → User Inbox
```

## Email Types

| Type | When Triggered | Template |
|------|----------------|----------|
| `signup` | User signs up with email/password | `_templates/signup.tsx` |
| `magiclink` | User requests OTP / forgot password | `_templates/magic-link.tsx` |
| `recovery` | Password reset (if using `resetPasswordForEmail`) | `_templates/recovery.tsx` |
| `invite` | Admin invites a user | `_templates/invite.tsx` |
| `email_change` | User changes their email | `_templates/email-change.tsx` |

> **Note**: Our app uses `signInWithOtp()` for password reset, which sends `magiclink` type, not `recovery`.

## Project Structure

```
send-email/
├── index.ts              # Main handler - routes emails to templates
├── _templates/
│   ├── base.tsx          # Shared layout, styles, and components
│   ├── signup.tsx        # Email confirmation
│   ├── magic-link.tsx    # OTP / forgot password
│   ├── recovery.tsx      # Password reset (if used)
│   ├── invite.tsx        # User invitation
│   └── email-change.tsx  # Email change confirmation
└── README.md
```

## Updating Templates

### 1. Edit the template file

Templates are React components using [@react-email/components](https://react.email/docs/introduction).

```tsx
// _templates/magic-link.tsx
import * as React from "npm:react@18.3.1";
import { BaseEmail, EmailHeading, EmailParagraph, EmailCode } from "./base.tsx";

export const MagicLinkEmail = ({ token }) => (
  <BaseEmail preview="Your Reliant verification code">
    <EmailHeading>Your verification code</EmailHeading>
    <EmailParagraph>Enter this code in the app to continue:</EmailParagraph>
    <EmailCode code={token} label="Your code" />
    <EmailParagraph muted>
      This code expires in 1 hour.
    </EmailParagraph>
  </BaseEmail>
);
```

### 2. Deploy changes

```bash
cd /path/to/project
supabase functions deploy send-email --no-verify-jwt
```

### 3. Test

Trigger the email flow in the app and check your inbox.

## Available Components (from base.tsx)

| Component | Props | Description |
|-----------|-------|-------------|
| `BaseEmail` | `preview`, `children` | Wrapper with logo, footer, styling |
| `EmailHeading` | `children` | Centered heading |
| `EmailParagraph` | `children`, `muted?` | Body text (muted for secondary text) |
| `EmailCode` | `code`, `label?` | Large code display box |
| `EmailButton` | `href`, `children` | CTA button (not currently used) |
| `EmailWarning` | `children` | Warning/info box |

## Styling

Brand colors and styles are defined in `_templates/base.tsx`:

```tsx
export const colors = {
  primary: "#6366f1",    // Indigo - brand color
  text: "#1e293b",       // Dark text
  textMuted: "#64748b",  // Secondary text
  // ...
};
```

## Secrets

Required environment variables (set via `supabase secrets set`):

| Secret | Description |
|--------|-------------|
| `RESEND_API_KEY` | Your Resend API key (starts with `re_`) |
| `SEND_EMAIL_HOOK_SECRET` | Webhook secret from Supabase Auth Hooks dashboard |

### Setting secrets

```bash
supabase secrets set RESEND_API_KEY=re_xxxxxxxxx
supabase secrets set SEND_EMAIL_HOOK_SECRET=xxxxxxxxx
```

> **Note**: The hook secret from the dashboard looks like `v1,whsec_xxxxx`. Remove the `v1,whsec_` prefix when setting the secret.

## Configuration

### Supabase Dashboard

1. Go to **Auth > Hooks**
2. Enable **Send Email** hook
3. Set URL: `https://dash.reliantlabs.io/functions/v1/send-email`
4. Generate and save the webhook secret

### Custom Domain Notes

If you are using a Supabase custom domain, keep these values aligned:

- Hook URL should use your custom domain (example above)
- `SUPABASE_URL` Edge Function secret should match the same domain (used to build verification links)
- Supabase Auth redirect allow-list should include app callback URLs (for Reliant: `http://127.0.0.1:*/auth/callback` as the desktop OAuth callback, plus local dev URLs)

### Sender Address

Emails are sent from: `Reliant <hello@auth.reliantlabs.io>`

To change this, edit `index.ts`:

```tsx
await resend.emails.send({
  from: "Reliant <hello@auth.reliantlabs.io>",  // Change this
  to: [user.email],
  subject: subjects[email_action_type],
  html,
});
```

## Debugging

### Check logs

```bash
# View logs in dashboard
https://supabase.com/dashboard/project/YOUR_PROJECT_REF/functions/send-email/logs
```

### Common issues

| Issue | Cause | Fix |
|-------|-------|-----|
| Old template showing | Didn't deploy | Run `supabase functions deploy send-email --no-verify-jwt` |
| Webhook verification failed | Wrong secret | Re-copy secret from dashboard, remove `v1,whsec_` prefix |
| Email not sent | Resend API key invalid | Check `RESEND_API_KEY` is set correctly |
| Wrong email type | App uses different auth method | Check logs for actual `email_action_type` |

## Adding a New Template

1. Create `_templates/new-type.tsx`:

```tsx
import * as React from "npm:react@18.3.1";
import { BaseEmail, EmailHeading, EmailParagraph, EmailCode } from "./base.tsx";

export const NewTypeEmail = ({ token }) => (
  <BaseEmail preview="Preview text">
    <EmailHeading>Heading</EmailHeading>
    <EmailParagraph>Body text</EmailParagraph>
    <EmailCode code={token} />
  </BaseEmail>
);
```

2. Import in `index.ts`:

```tsx
import { NewTypeEmail } from "./_templates/new-type.tsx";
```

3. Add to subjects:

```tsx
const subjects = {
  // ...
  new_type: "Subject line",
};
```

4. Add case in switch:

```tsx
case "new_type":
  html = await renderAsync(React.createElement(NewTypeEmail, templateProps));
  break;
```

5. Deploy: `supabase functions deploy send-email --no-verify-jwt`

## Resources

- [Supabase Send Email Hook](https://supabase.com/docs/guides/auth/auth-hooks/send-email-hook)
- [React Email Components](https://react.email/docs/components/html)
- [Resend Documentation](https://resend.com/docs)