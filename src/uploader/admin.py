from django.contrib import admin

from .models import Iban, Bic, Currency, Optionee, Transaction

# admin.site.register(Iban)
# admin.site.register(Bic)
# admin.site.register(Currency)

@admin.register(Iban, Bic, Currency, Optionee, Transaction)
class TransactionsAdmin(admin.ModelAdmin):
    pass