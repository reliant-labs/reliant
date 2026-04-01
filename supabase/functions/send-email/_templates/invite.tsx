import * as React from "npm:react@18.3.1";
import {
  BaseEmail,
  EmailHeading,
  EmailParagraph,
  EmailCode,
} from "./base.tsx";

interface InviteEmailProps {
  token: string;
  confirmationUrl: string;
  userName: string;
  email: string;
  site_url: string;
}

export const InviteEmail: React.FC<InviteEmailProps> = ({
  token,
}) => (
  <BaseEmail preview="You've been invited to join Reliant">
    <EmailHeading>You've been invited!</EmailHeading>

    <EmailParagraph>
      You've been invited to join Reliant, the intelligent coding assistant that helps you build software faster.
    </EmailParagraph>

    <EmailParagraph>
      Enter this code in the app to accept the invitation:
    </EmailParagraph>

    <EmailCode code={token} label="Your invitation code" />

    <EmailParagraph muted>
      This invitation expires in 24 hours. If you weren't expecting this invitation, you can safely ignore this email.
    </EmailParagraph>
  </BaseEmail>
);

export default InviteEmail;
