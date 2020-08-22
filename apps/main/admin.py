from django.contrib import admin

from .models import Iban, Bic, BookingType, Currency, Optionee, Transaction


@admin.register(Iban, Bic, BookingType, Currency, Optionee, Transaction)
class TransactionsAdmin(admin.ModelAdmin):
    pass