import React from "npm:react@18.3.1";
import { Webhook } from "https://esm.sh/standardwebhooks@1.0.0";
import { Resend } from "npm:resend@4.0.0";
import { renderAsync } from "npm:@react-email/components@0.0.22";

// Import email templates
import { SignupEmail } from "./_templates/signup.tsx";
import { MagicLinkEmail } from "./_templates/magic-link.tsx";
import { RecoveryEmail } from "./_templates/recovery.tsx";
import { InviteEmail } from "./_templates/invite.tsx";
import { EmailChangeEmail } from "./_templates/email-change.tsx";

const resend = new Resend(Deno.env.get("RESEND_API_KEY") as string);
const rawHookSecret = Deno.env.get("SEND_EMAIL_HOOK_SECRET") as string;

// Extract the base64 secret from format "v1,whsec_<base64>"
// The standardwebhooks library expects just "whsec_<base64>" or raw base64
function extractWebhookSecret(secret: string): string {
  if (!secret) return secret;

  // If it starts with "v1," prefix, remove it
  if (secret.startsWith("v1,")) {
    return secret.substring(3); // Remove "v1,"
  }
  return secret;
}

const hookSecret = extractWebhookSecret(rawHookSecret);

// Types for the webhook payload
interface User {
  id: string;
  email: string;
  new_email?: string;
  email_change?: string;
  user_metadata?: {
    full_name?: string;
    [key: string]: unknown;
  };
}

interface EmailData {
  token: string;
  token_hash: string;
  redirect_to: string;
  email_action_type: string;
  site_url: string;
  token_new: string;
  token_hash_new: string;
}

interface WebhookPayload {
  user: User;
  email_data: EmailData;
}

// Email subjects by type
const subjects: Record<string, string> = {
  signup: "Confirm your email",
  magiclink: "Your Reliant verification code",
  recovery: "Reset your password",
  invite: "You've been invited to Reliant",
  email_change: "Confirm your email change",
};

Deno.serve(async (req) => {
  if (req.method !== "POST") {
    return new Response("Method not allowed", { status: 405 });
  }

  // Check if hookSecret is available
  if (!hookSecret) {
    console.error("CRITICAL: SEND_EMAIL_HOOK_SECRET is not set!");
    return new Response(
      JSON.stringify({
        error: {
          http_code: 500,
          message: "Server misconfiguration: missing webhook secret",
        },
      }),
      {
        status: 500,
        headers: { "Content-Type": "application/json" },
      }
    );
  }

  const payload = await req.text();
  const headers = Object.fromEntries(req.headers);

  // Verify webhook signature
  const wh = new Webhook(hookSecret);
  let webhookData: WebhookPayload;

  try {
    webhookData = wh.verify(payload, headers) as WebhookPayload;
  } catch (error) {
    console.error("Webhook verification failed:", error);
    return new Response(
      JSON.stringify({
        error: {
          http_code: 401,
          message: "Webhook verification failed",
        },
      }),
      {
        status: 401,
        headers: { "Content-Type": "application/json" },
      }
    );
  }

  const { user, email_data } = webhookData;
  const {
    token,
    token_hash,
    redirect_to,
    email_action_type,
    site_url,
    token_new,
    token_hash_new,
  } = email_data;

  // Log the email type for debugging
  console.log(`Processing email: type=${email_action_type}, user_id=${user.id}`);

  // Determine recipient
  let recipient = user.email;

  // For anonymous users upgrading, user.email is null/empty, but new_email should be present
  if (email_action_type === 'email_change' && (!recipient || recipient.trim() === '')) {
    recipient = user.new_email || user.email_change || '';
    console.log(`Anonymous upgrade detected. Using new_email as recipient: ${recipient}`);
  }

  // Validate recipient
  if (!recipient || recipient.trim() === '') {
    const errorMsg = "No valid recipient email found in user payload";
    console.error(errorMsg, { user });
    return new Response(
      JSON.stringify({
        error: {
          http_code: 422,
          message: errorMsg,
        },
      }),
      {
        status: 422,
        headers: { "Content-Type": "application/json" },
      }
    );
  }

  // Build confirmation URL
  const supabaseUrl = Deno.env.get("SUPABASE_URL") ?? "";
  const confirmationUrl = `${supabaseUrl}/auth/v1/verify?token=${token_hash}&type=${email_action_type}&redirect_to=${redirect_to}`;

  // Template props
  const templateProps = {
    token,
    token_hash,
    confirmationUrl,
    redirect_to,
    site_url,
    email: recipient,
    userName: user.user_metadata?.full_name || recipient.split("@")[0],
  };

  try {
    // Render the appropriate template
    let html: string;

    switch (email_action_type) {
      case "signup":
        html = await renderAsync(React.createElement(SignupEmail, templateProps));
        break;
      case "magiclink":
        html = await renderAsync(React.createElement(MagicLinkEmail, templateProps));
        break;
      case "recovery":
        html = await renderAsync(React.createElement(RecoveryEmail, templateProps));
        break;
      case "invite":
        html = await renderAsync(React.createElement(InviteEmail, templateProps));
        break;
      case "email_change":
        html = await renderAsync(
          React.createElement(EmailChangeEmail, {
            ...templateProps,
            token_new,
            token_hash_new,
          })
        );
        break;
      default:
        // Fallback to magic link template for unknown types
        html = await renderAsync(React.createElement(MagicLinkEmail, templateProps));
    }

    // Send email via Resend
    const { error } = await resend.emails.send({
      from: "Reliant <hello@auth.reliantlabs.io>",
      to: [recipient],
      subject: subjects[email_action_type] || "Notification from Reliant",
      html,
    });

    if (error) {
      console.error("Resend error:", error);
      throw error;
    }

    console.log(`Email sent successfully: ${email_action_type} to ${recipient}`);

    return new Response(JSON.stringify({}), {
      status: 200,
      headers: { "Content-Type": "application/json" },
    });
  } catch (error) {
    console.error("Error sending email:", error);
    return new Response(
      JSON.stringify({
        error: {
          http_code: (error as { code?: number }).code || 500,
          message: (error as Error).message || "Failed to send email",
        },
      }),
      {
        status: 500,
        headers: { "Content-Type": "application/json" },
      }
    );
  }
});
