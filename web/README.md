# Reliant Web Application

A modern web UI for the Reliant AI coding assistant, built with React, TypeScript, and Vite.

## Tech Stack

- **React 19** - Latest version of the React library
- **TypeScript** - For type safety and better developer experience
- **Vite** - Fast build tool and development server
- **Tailwind CSS** - Utility-first CSS framework
- **Zustand** - Lightweight state management
- **gRPC Streaming** - Real-time communication with the backend via Connect protocol
- **Playwright** - End-to-end testing
- **Vitest** - Unit and integration testing

## Getting Started

### Prerequisites
- Node.js 18+ installed
- Go 1.25+ installed (for the API server)

### Installation

1. Install dependencies:
```bash
cd web
npm install
```

2. Start the development server:
```bash
npm run dev
```

The web app will be available at `http://localhost:5173`

### Available Scripts

- `npm run dev` - Start the development server
- `npm run build` - Build for production
- `npm run preview` - Preview the production build
- `npm run lint` - Run ESLint
- `npm run test` - Run unit and integration tests
- `npm run test:watch` - Run tests in watch mode
- `npm run test:e2e` - Run end-to-end tests with Playwright
- `npm run test:e2e:ui` - Run Playwright tests with UI

## Project Structure

```
web/
├── e2e/                # End-to-end tests
├── public/             # Static assets
├── src/
│   ├── api/            # API client and gRPC streaming connection
│   ├── assets/         # Images and other assets
│   ├── components/     # React components
│   ├── lib/            # Utility functions
│   ├── store/          # Zustand state management
│   └── types/          # TypeScript type definitions
```

## Features

- **Chat Interface** - Interactive chat with AI assistant
- **Tool Execution** - Display and interact with AI tool usage
- **Permission Management** - Control AI access to system resources
- **Session Metrics** - Track session performance and usage
- **gRPC Streaming** - Real-time updates from the backend via Connect protocol
- **Responsive Design** - Works on desktop and mobile devices

## Development

The web app communicates with the Reliant API server. Make sure the API server is running:

```bash
# From the project root
npm run dev
```