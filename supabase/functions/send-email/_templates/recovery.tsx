import * as React from "npm:react@18.3.1";
import {
  BaseEmail,
  EmailHeading,
  EmailParagraph,
  EmailCode,
} from "./base.tsx";

interface RecoveryEmailProps {
  token: string;
  confirmationUrl: string;
  userName: string;
  email: string;
}

export const RecoveryEmail: React.FC<RecoveryEmailProps> = ({
  token,
}) => (
  <BaseEmail preview="Reset your Reliant password">
    <EmailHeading>Reset your password</EmailHeading>

    <EmailParagraph>
      Enter this code in the app to reset your password:
    </EmailParagraph>

    <EmailCode code={token} label="Your reset code" />

    <EmailParagraph muted>
      This code expires in 10 minutes. If you didn't request a password reset, you can safely ignore this email.
    </EmailParagraph>
  </BaseEmail>
);

export default RecoveryEmail;
