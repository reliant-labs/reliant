import {
  Body,
  Container,
  Head,
  Html,
  Link,
  Preview,
  Section,
  Text,
} from "npm:@react-email/components@0.0.22";
import * as React from "npm:react@18.3.1";

// Reliant brand colors
export const colors = {
  primary: "#6366f1", // Indigo
  primaryDark: "#4f46e5",
  background: "#f6f9fc",
  cardBackground: "#ffffff",
  text: "#1e293b",
  textMuted: "#525f7f",
  border: "#e6ebf1",
  codeBackground: "#f1f5f9",
};

// Shared styles - Stripe-inspired
export const styles = {
  main: {
    backgroundColor: colors.background,
    fontFamily:
      '-apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, "Helvetica Neue", Ubuntu, sans-serif',
  },
  outerContainer: {
    margin: "0 auto",
    padding: "45px 0",
    maxWidth: "600px",
  },
  card: {
    backgroundColor: colors.cardBackground,
    borderRadius: "8px",
    border: `1px solid ${colors.border}`,
    padding: "40px 48px",
    margin: "0 20px",
  },
  logo: {
    fontSize: "24px",
    fontWeight: "700",
    color: colors.primary,
    margin: "0 0 32px 0",
    textAlign: "left" as const,
  },
  heading: {
    color: colors.text,
    fontSize: "20px",
    fontWeight: "600",
    textAlign: "left" as const,
    margin: "0 0 20px",
    padding: "0",
  },
  paragraph: {
    color: colors.text,
    fontSize: "16px",
    lineHeight: "28px",
    margin: "0 0 16px",
    textAlign: "left" as const,
  },
  paragraphMuted: {
    color: colors.textMuted,
    fontSize: "14px",
    lineHeight: "24px",
    margin: "16px 0 0",
    textAlign: "left" as const,
  },
  codeContainer: {
    backgroundColor: colors.codeBackground,
    borderRadius: "6px",
    padding: "20px 24px",
    textAlign: "left" as const,
    margin: "24px 0",
  },
  code: {
    color: colors.text,
    fontSize: "28px",
    fontWeight: "700",
    letterSpacing: "3px",
    fontFamily: 'Menlo, Monaco, "Courier New", monospace',
    margin: "0",
  },
  codeLabel: {
    color: colors.textMuted,
    fontSize: "12px",
    fontWeight: "600",
    textTransform: "uppercase" as const,
    letterSpacing: "0.5px",
    marginBottom: "8px",
  },
  footer: {
    color: colors.textMuted,
    fontSize: "13px",
    lineHeight: "22px",
    textAlign: "left" as const,
    margin: "32px 20px 0",
  },
  footerLink: {
    color: colors.primary,
    textDecoration: "none",
  },
  link: {
    color: colors.primary,
    textDecoration: "none",
  },
  signature: {
    color: colors.text,
    fontSize: "16px",
    lineHeight: "28px",
    margin: "24px 0 0",
    textAlign: "left" as const,
  },
};

interface BaseEmailProps {
  preview: string;
  children: React.ReactNode;
}

export const BaseEmail: React.FC<BaseEmailProps> = ({ preview, children }) => (
  <Html>
    <Head />
    <Preview>{preview}</Preview>
    <Body style={styles.main}>
      <Container style={styles.outerContainer}>
        {/* Card */}
        <Section style={styles.card}>
          {/* Logo */}
          <Text style={styles.logo}>Reliant</Text>

          {/* Content */}
          {children}

          {/* Signature */}
          <Text style={styles.signature}>— The Reliant team</Text>
        </Section>

        {/* Footer - outside card */}
        <Text style={styles.footer}>
          You're receiving this email because you have an account with{" "}
          <Link href="https://reliantlabs.io" style={styles.footerLink}>
            Reliant
          </Link>
          . If you have questions, contact us at{" "}
          <Link href="mailto:support@reliantlabs.io" style={styles.footerLink}>
            support@reliantlabs.io
          </Link>
          .
        </Text>
      </Container>
    </Body>
  </Html>
);

// Reusable components
export const EmailHeading: React.FC<{ children: React.ReactNode }> = ({
  children,
}) => <Text style={styles.heading}>{children}</Text>;

export const EmailParagraph: React.FC<{
  children: React.ReactNode;
  muted?: boolean;
}> = ({ children, muted }) => (
  <Text style={muted ? styles.paragraphMuted : styles.paragraph}>{children}</Text>
);

export const EmailButton: React.FC<{
  href: string;
  children: React.ReactNode;
}> = ({ href, children }) => (
  <Section style={{ margin: "28px 0" }}>
    <Link
      href={href}
      style={{
        backgroundColor: colors.primary,
        borderRadius: "6px",
        color: "#ffffff",
        display: "inline-block",
        fontSize: "15px",
        fontWeight: "600",
        padding: "12px 24px",
        textDecoration: "none",
      }}
    >
      {children}
    </Link>
  </Section>
);

export const EmailCode: React.FC<{ code: string; label?: string }> = ({
  code,
  label = "Your verification code",
}) => (
  <Section style={styles.codeContainer}>
    <Text style={styles.codeLabel}>{label}</Text>
    <Text style={styles.code}>{code}</Text>
  </Section>
);

export default BaseEmail;
