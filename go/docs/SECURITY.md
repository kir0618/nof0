# Security Best Practices

## Overview

This document outlines security best practices for the nof0 trading system.

## Credential Management

### ⚠️ NEVER Commit Sensitive Information

**DO NOT** commit the following to version control:
- Real database credentials (usernames, passwords, connection strings)
- API keys (LLM providers, exchange APIs)
- Private keys (trading accounts, wallets)
- Redis passwords
- Production configuration files with secrets

### ✅ Use Environment Variables

All sensitive configuration should be injected via environment variables:

```bash
# Example .env file (this file is gitignored)
HYPERLIQUID_PRIVATE_KEY=your_private_key_here
ZENMUX_API_KEY=your_api_key_here
Postgres__DataSource=postgres://user:pass@host:5432/db
Cache__0__Pass=your_redis_password
```

### ✅ Use Placeholders in Documentation

When writing documentation with examples, always use placeholders:

```bash
# ✅ GOOD - Uses placeholders
export Postgres__DataSource="postgres://YOUR_DB_USER:YOUR_DB_PASSWORD@YOUR_DB_HOST:5432/postgres"

# ❌ BAD - Contains real credentials
export Postgres__DataSource="postgres://realuser:RealPass123@prod-db.example.com:5432/postgres"
```

## Git Hooks

### Installing Pre-commit Hook

A pre-commit hook is provided to scan for sensitive information before commits:

```bash
# Install the hook
cp .git-hooks-example/pre-commit .git/hooks/pre-commit
chmod +x .git/hooks/pre-commit
```

The hook will automatically scan for:
- Database connection strings with passwords
- API keys and secrets
- Private keys
- Password assignments
- Supabase URLs (with warnings)

### Bypassing the Hook (Not Recommended)

Only bypass if you're certain no sensitive data is present:

```bash
git commit --no-verify
```

## Credential Rotation

If credentials are accidentally committed:

1. **Immediately rotate the exposed credentials**
   - Change database passwords
   - Regenerate API keys
   - Create new private keys

2. **Remove from Git history**
   ```bash
   # Using git-filter-repo (recommended)
   pip install git-filter-repo
   git filter-repo --path path/to/file --invert-paths

   # Force push (WARNING: rewrites history)
   git push origin --force --all
   ```

3. **Audit access logs**
   - Check database connection logs
   - Review API usage logs
   - Monitor for suspicious activity

## Production Deployment

### Secrets Management

Use a secrets management service for production:

- **AWS**: AWS Secrets Manager / Parameter Store
- **GCP**: Secret Manager
- **Azure**: Key Vault
- **Self-hosted**: HashiCorp Vault
- **Platform**: Railway/Render secrets

### Database Security

1. **Use TLS/SSL connections**
   ```
   ?sslmode=require
   ```

2. **Restrict network access**
   - Use VPC/private networks
   - Whitelist application IPs only

3. **Use strong credentials**
   - Minimum 32 characters
   - Include uppercase, lowercase, numbers, symbols
   - Rotate regularly (every 90 days)

4. **Principle of least privilege**
   - Application user should only have necessary permissions
   - No superuser access for applications

### API Security

1. **Rate limiting**: Enable on all API endpoints
2. **Authentication**: Implement JWT or API key authentication
3. **CORS**: Restrict to specific origins (no wildcards in production)
4. **HTTPS**: Always use TLS in production

## Monitoring

### Security Monitoring Checklist

- [ ] Enable database audit logging
- [ ] Monitor failed authentication attempts
- [ ] Set up alerts for unusual API usage
- [ ] Review access logs weekly
- [ ] Scan dependencies for vulnerabilities monthly

### Tools

```bash
# Scan Git history for secrets
pip install truffleHog
trufflehog --regex --entropy=False file://./

# Scan dependencies
go list -json -m all | nancy sleuth
```

## Incident Response

If a security incident occurs:

1. **Isolate**: Revoke compromised credentials immediately
2. **Assess**: Determine scope of breach
3. **Remediate**: Rotate all related credentials
4. **Review**: Conduct post-mortem and update procedures
5. **Notify**: Inform stakeholders if required

## Security Contacts

- Security issues: [Create private security advisory on GitHub]
- Production incidents: [Your incident response channel]

## Additional Resources

- [OWASP Top 10](https://owasp.org/www-project-top-ten/)
- [CWE Top 25](https://cwe.mitre.org/top25/)
- [Go Security Best Practices](https://go.dev/doc/security/best-practices)

---

**Last Updated**: 2025-01-06
**Review Frequency**: Quarterly
