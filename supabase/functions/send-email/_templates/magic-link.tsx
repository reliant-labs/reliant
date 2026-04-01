import * as React from "npm:react@18.3.1";
import {
  BaseEmail,
  EmailHeading,
  EmailParagraph,
  EmailCode,
} from "./base.tsx";

interface MagicLinkEmailProps {
  token: string;
  confirmationUrl: string;
  userName: string;
  email: string;
}

export const MagicLinkEmail: React.FC<MagicLinkEmailProps> = ({
  token,
}) => (
  <BaseEmail preview="Your Reliant verification code">
    <EmailHeading>Your verification code</EmailHeading>

    <EmailParagraph>
      Enter this code in the app to continue:
    </EmailParagraph>

    <EmailCode code={token} label="Your code" />

    <EmailParagraph muted>
      This code expires in 10 minutes. If you didn't request this code, you can safely ignore this email.
    </EmailParagraph>
  </BaseEmail>
);

export default MagicLinkEmail;
