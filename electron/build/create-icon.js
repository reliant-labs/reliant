// Simple script to create a basic icon
const fs = require('fs');
const path = require('path');

// Create a simple SVG icon
const svgIcon = `<?xml version="1.0" encoding="UTF-8"?>
<svg width="256" height="256" viewBox="0 0 256 256" xmlns="http://www.w3.org/2000/svg">
  <rect width="256" height="256" fill="#1e3a8a" rx="32"/>
  <circle cx="128" cy="128" r="80" fill="#3b82f6"/>
  <text x="128" y="148" font-family="Arial" font-size="80" font-weight="bold" fill="white" text-anchor="middle">R</text>
</svg>`;

// Write SVG to file
const iconPath = path.join(__dirname, 'icon.svg');
fs.writeFileSync(iconPath, svgIcon);

console.log('Created basic icon:', iconPath);

// Also create a simple base64 PNG as fallback
const simplePNG = Buffer.from('iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNkYPhfDwAChwGA60e6kgAAAABJRU5ErkJggg==', 'base64');
fs.writeFileSync(path.join(__dirname, 'icon.png'), simplePNG);

console.log('Created fallback PNG icon');