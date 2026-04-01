import * as React from "npm:react@18.3.1";
import {
  BaseEmail,
  EmailHeading,
  EmailParagraph,
  EmailCode,
} from "./base.tsx";

interface EmailChangeEmailProps {
  token: string;
  token_new?: string;
  confirmationUrl: string;
  userName: string;
  email: string;
}

export const EmailChangeEmail: React.FC<EmailChangeEmailProps> = ({
  token,
  token_new,
}) => (
  <BaseEmail preview="Confirm your email change for Reliant">
    <EmailHeading>Confirm email change</EmailHeading>

    <EmailParagraph>
      Enter this code in the app to confirm your email change:
    </EmailParagraph>

    <EmailCode code={token} label="Your confirmation code" />

    {token_new && (
      <EmailParagraph muted>
        You may also need to enter this code for your new email: <strong>{token_new}</strong>
      </EmailParagraph>
    )}

    <EmailParagraph muted>
      This code expires in 10 minutes. If you didn't request this change, please secure your account immediately.
    </EmailParagraph>
  </BaseEmail>
);

export default EmailChangeEmail;
