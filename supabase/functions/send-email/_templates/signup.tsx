import * as React from "npm:react@18.3.1";
import {
  BaseEmail,
  EmailHeading,
  EmailParagraph,
  EmailCode,
} from "./base.tsx";

interface SignupEmailProps {
  token: string;
  confirmationUrl: string;
  userName: string;
  email: string;
}

export const SignupEmail: React.FC<SignupEmailProps> = ({
  token,
}) => (
  <BaseEmail preview="Confirm your email to get started with Reliant">
    <EmailHeading>Confirm your email</EmailHeading>

    <EmailParagraph>
      Welcome to Reliant! Enter this code in the app to confirm your email:
    </EmailParagraph>

    <EmailCode code={token} label="Your verification code" />

    <EmailParagraph muted>
      This code expires in 10 minutes. If you didn't create an account with Reliant, you can safely ignore this email.
    </EmailParagraph>
  </BaseEmail>
);

export default SignupEmail;
