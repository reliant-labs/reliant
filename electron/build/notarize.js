// notarize.js - macOS notarization script
const { notarize } = require('@electron/notarize');

exports.default = async function notarizing(context) {
  const { electronPlatformName, appOutDir } = context;
  
  if (electronPlatformName !== 'darwin') {
    return;
  }

  const appName = context.packager.appInfo.productFilename;

  const appleId = process.env.APPLE_ID;
  const appleIdPassword = process.env.APPLE_APP_SPECIFIC_PASSWORD;
  const teamId = process.env.APPLE_TEAM_ID;

  if (!appleId || !appleIdPassword || !teamId) {
    console.warn('⚠️ Skipping notarization: Apple credentials not set');
    console.warn(`   APPLE_ID: ${appleId ? '✓ Set' : '✗ Missing'}`);
    console.warn(`   APPLE_APP_SPECIFIC_PASSWORD: ${appleIdPassword ? '✓ Set' : '✗ Missing'}`);
    console.warn(`   APPLE_TEAM_ID: ${teamId ? '✓ Set' : '✗ Missing'}`);
    return;
  }

  console.log('🍎 Starting notarization process...');
  console.log(`   App: ${appName}.app`);
  console.log(`   Bundle ID: com.reliantlabs.reliant`);
  console.log(`   Team ID: ${teamId}`);
  console.log(`   Apple ID: ${appleId}`);
  console.log(`   App Path: ${appOutDir}/${appName}.app`);
  
  try {
    // Add retry logic and better error handling
    let attempts = 0;
    const maxAttempts = 3;
    
    while (attempts < maxAttempts) {
      attempts++;
      console.log(`🔄 Notarization attempt ${attempts}/${maxAttempts}...`);
      
      try {
        await notarize({
          tool: 'notarytool',
          appBundleId: 'com.reliantlabs.reliant',
          appPath: `${appOutDir}/${appName}.app`,
          appleId: appleId,
          appleIdPassword: appleIdPassword,
          teamId: teamId,
        });
        
        console.log('✅ Notarization successful!');
        return; // Success, exit the function
        
      } catch (attemptError) {
        console.log(`❌ Attempt ${attempts} failed:`, attemptError.message);
        
        // Check if it's a credential issue (don't retry)
        if (attemptError.message.includes('Authentication failed') || 
            attemptError.message.includes('Invalid credentials') ||
            attemptError.message.includes('401')) {
          console.error('🚫 Authentication failed - check your Apple ID credentials');
          throw attemptError;
        }
        
        // Check if it's a JSON parsing error (common network issue, can retry)
        if (attemptError.message.includes('Unexpected token') || 
            attemptError.message.includes('not valid JSON')) {
          console.log('🔄 Network/parsing error detected, will retry...');
          if (attempts < maxAttempts) {
            console.log(`⏳ Waiting 30 seconds before retry...`);
            await new Promise(resolve => setTimeout(resolve, 30000));
            continue;
          }
        }
        
        // If it's the last attempt or an unrecoverable error, throw
        if (attempts >= maxAttempts) {
          throw attemptError;
        }
      }
    }
  } catch (error) {
    console.error('❌ Notarization failed after all attempts:');
    console.error('   Error:', error.message);
    
    // Provide helpful debugging information
    if (error.message.includes('not valid JSON')) {
      console.error('');
      console.error('📋 Troubleshooting steps:');
      console.error('   1. Check Apple system status: https://developer.apple.com/system-status/');
      console.error('   2. Verify your Apple ID credentials are correct');
      console.error('   3. Ensure your app-specific password is valid');
      console.error('   4. Try again later - Apple servers may be experiencing issues');
      console.error('');
      console.error('⚠️  This is often a temporary Apple server issue. The app is signed correctly.');
      console.error('   You can manually notarize later using: xcrun notarytool submit');
    }
    
    throw error;
  }
};